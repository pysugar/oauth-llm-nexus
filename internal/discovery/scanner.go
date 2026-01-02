package discovery

import (
	"log"
	"path/filepath"
)

// ScanResult holds the result of scanning all sources
type ScanResult struct {
	Credentials []Credential `json:"credentials"`
	Errors      []ScanError  `json:"errors,omitempty"`
}

// ScanError represents an error encountered during scanning
type ScanError struct {
	Source string `json:"source"`
	Path   string `json:"path"`
	Error  string `json:"error"`
}

// ScanAll scans all known sources for credentials
func ScanAll() *ScanResult {
	result := &ScanResult{
		Credentials: make([]Credential, 0),
		Errors:      make([]ScanError, 0),
	}

	for _, source := range Sources {
		creds, errs := scanSource(source)
		result.Credentials = append(result.Credentials, creds...)
		result.Errors = append(result.Errors, errs...)
	}

	log.Printf("🔍 Discovery: Found %d credentials from %d sources", len(result.Credentials), len(Sources))
	return result
}

// scanSource scans a single source for credentials
func scanSource(source Source) ([]Credential, []ScanError) {
	var credentials []Credential
	var errors []ScanError

	for _, pathPattern := range source.ConfigPaths {
		expanded := expandPath(pathPattern)
		
		// Handle wildcards
		matches, err := filepath.Glob(expanded)
		if err != nil {
			errors = append(errors, ScanError{
				Source: source.Name,
				Path:   expanded,
				Error:  "Glob error: " + err.Error(),
			})
			continue
		}

		if len(matches) == 0 {
			continue
		}

		for _, path := range matches {
			// Parse credentials
			cred, err := source.Parser(path)
			if err != nil {
				errors = append(errors, ScanError{
					Source: source.Name,
					Path:   path,
					Error:  err.Error(),
				})
				continue
			}

			if cred != nil && (cred.AccessToken != "" || cred.RefreshToken != "") {
				log.Printf("🔍 Found credentials from %s: %s", source.Name, path)
				credentials = append(credentials, *cred)
			}
		}
	}

	return credentials, errors
}

// MaskToken returns a masked version of a token for display
func MaskToken(token string) string {
	if len(token) <= 8 {
		return "***"
	}
	return token[:4] + "..." + token[len(token)-4:]
}

// MaskCredential returns a copy of the credential with masked tokens
func MaskCredential(cred Credential) Credential {
	masked := cred
	masked.AccessToken = MaskToken(cred.AccessToken)
	masked.RefreshToken = MaskToken(cred.RefreshToken)
	return masked
}
