package monitor

import (
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/pysugar/oauth-llm-nexus/internal/db/models"
	"gorm.io/gorm"
)

const (
	// MaxRequestBodySize limits request body storage to 1MB
	MaxRequestBodySize = 1024 * 1024
	// MaxResponseBodySize limits response body storage to 512KB
	MaxResponseBodySize = 512 * 1024
	// MaxMemoryLogs limits in-memory log cache
	MaxMemoryLogs = 100
)

// ProxyMonitor manages API request logging and statistics
type ProxyMonitor struct {
	db      *gorm.DB
	enabled atomic.Bool

	// In-memory cache for recent logs (thread-safe)
	recentLogs []models.RequestLog
	logsMu     sync.RWMutex

	// In-memory stats (updated atomically)
	totalRequests atomic.Int64
	successCount  atomic.Int64
	errorCount    atomic.Int64
}

// NewProxyMonitor creates a new ProxyMonitor instance
func NewProxyMonitor(db *gorm.DB) *ProxyMonitor {
	pm := &ProxyMonitor{
		db:         db,
		recentLogs: make([]models.RequestLog, 0, MaxMemoryLogs),
	}

	// Auto-migrate the RequestLog table
	if err := db.AutoMigrate(&models.RequestLog{}); err != nil {
		log.Printf("[Monitor] Failed to migrate RequestLog table: %v", err)
	}

	// Load initial stats from DB
	pm.loadStatsFromDB()

	// Default to disabled
	pm.enabled.Store(false)

	return pm
}

// SetEnabled enables or disables request logging
func (pm *ProxyMonitor) SetEnabled(enabled bool) {
	pm.enabled.Store(enabled)
	log.Printf("[Monitor] Logging %s", map[bool]string{true: "enabled", false: "disabled"}[enabled])
}

// IsEnabled returns whether logging is enabled
func (pm *ProxyMonitor) IsEnabled() bool {
	return pm.enabled.Load()
}

// LogRequest logs an API request (async, non-blocking)
func (pm *ProxyMonitor) LogRequest(logEntry models.RequestLog) {
	if !pm.IsEnabled() {
		return
	}

	// Generate ID if not set
	if logEntry.ID == "" {
		logEntry.ID = uuid.New().String()
	}

	// Set timestamp if not set
	if logEntry.Timestamp == 0 {
		logEntry.Timestamp = time.Now().UnixMilli()
	}

	// Truncate bodies if too large
	if len(logEntry.RequestBody) > MaxRequestBodySize {
		logEntry.RequestBody = logEntry.RequestBody[:MaxRequestBodySize] + "...[truncated]"
	}
	if len(logEntry.ResponseBody) > MaxResponseBodySize {
		logEntry.ResponseBody = logEntry.ResponseBody[:MaxResponseBodySize] + "...[truncated]"
	}

	// Update in-memory stats
	pm.totalRequests.Add(1)
	if logEntry.Status >= 200 && logEntry.Status < 400 {
		pm.successCount.Add(1)
	} else {
		pm.errorCount.Add(1)
	}

	// Add to in-memory cache
	pm.logsMu.Lock()
	pm.recentLogs = append([]models.RequestLog{logEntry}, pm.recentLogs...)
	if len(pm.recentLogs) > MaxMemoryLogs {
		pm.recentLogs = pm.recentLogs[:MaxMemoryLogs]
	}
	pm.logsMu.Unlock()

	// Async save to DB
	go func(entry models.RequestLog) {
		if err := pm.db.Create(&entry).Error; err != nil {
			log.Printf("[Monitor] Failed to save log: %v", err)
		}
	}(logEntry)
}

// GetLogs returns recent request logs with optional time filter
func (pm *ProxyMonitor) GetLogs(limit int, sinceMinutes int) []models.RequestLog {
	if limit <= 0 {
		limit = 100
	}

	var logs []models.RequestLog
	query := pm.db.Order("timestamp DESC").Limit(limit)

	// Apply time filter if specified
	if sinceMinutes > 0 {
		sinceTime := time.Now().Add(-time.Duration(sinceMinutes) * time.Minute).UnixMilli()
		query = query.Where("timestamp >= ?", sinceTime)
	}

	if err := query.Find(&logs).Error; err != nil {
		log.Printf("[Monitor] Failed to get logs from DB: %v", err)
		// Fallback to memory
		pm.logsMu.RLock()
		defer pm.logsMu.RUnlock()
		if limit > len(pm.recentLogs) {
			limit = len(pm.recentLogs)
		}
		return pm.recentLogs[:limit]
	}
	return logs
}

// GetLogsWithPagination returns logs with pagination support for history view
func (pm *ProxyMonitor) GetLogsWithPagination(page, pageSize int, search string) ([]models.RequestLog, int64) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 100
	}

	var logs []models.RequestLog
	var total int64

	query := pm.db.Model(&models.RequestLog{})

	// Apply search filter
	if search != "" {
		searchPattern := "%" + search + "%"
		query = query.Where("model LIKE ? OR url LIKE ? OR account_email LIKE ? OR error LIKE ?",
			searchPattern, searchPattern, searchPattern, searchPattern)
	}

	// Count total
	query.Count(&total)

	// Get page
	offset := (page - 1) * pageSize
	if err := query.Order("timestamp DESC").Offset(offset).Limit(pageSize).Find(&logs).Error; err != nil {
		log.Printf("[Monitor] Failed to get logs with pagination: %v", err)
		return nil, 0
	}

	return logs, total
}

// GetStats returns aggregated request statistics
func (pm *ProxyMonitor) GetStats() models.RequestStats {
	return models.RequestStats{
		TotalRequests: pm.totalRequests.Load(),
		SuccessCount:  pm.successCount.Load(),
		ErrorCount:    pm.errorCount.Load(),
	}
}

// Clear clears all logs from memory and database
func (pm *ProxyMonitor) Clear() error {
	// Clear memory
	pm.logsMu.Lock()
	pm.recentLogs = pm.recentLogs[:0]
	pm.logsMu.Unlock()

	// Reset stats
	pm.totalRequests.Store(0)
	pm.successCount.Store(0)
	pm.errorCount.Store(0)

	// Clear DB
	if err := pm.db.Exec("DELETE FROM request_logs").Error; err != nil {
		log.Printf("[Monitor] Failed to clear logs: %v", err)
		return err
	}

	log.Printf("[Monitor] All logs cleared")
	return nil
}

// loadStatsFromDB loads initial statistics from database
func (pm *ProxyMonitor) loadStatsFromDB() {
	var total, success, errors int64

	pm.db.Model(&models.RequestLog{}).Count(&total)
	pm.db.Model(&models.RequestLog{}).Where("status >= 200 AND status < 400").Count(&success)
	pm.db.Model(&models.RequestLog{}).Where("status < 200 OR status >= 400").Count(&errors)

	pm.totalRequests.Store(total)
	pm.successCount.Store(success)
	pm.errorCount.Store(errors)

	log.Printf("[Monitor] Loaded stats: total=%d, success=%d, errors=%d", total, success, errors)
}
