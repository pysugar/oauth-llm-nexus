#!/usr/bin/env python3
"""
Antigravity 配额查询脚本

从本地 Antigravity 客户端读取 OAuth 凭证，通过 Cloud Code API 获取模型配额信息。

使用方法:
    uv run -- python scripts/antigravity_quota.py           # 表格输出
    uv run -- python scripts/antigravity_quota.py --json    # JSON 输出
    uv run -- python scripts/antigravity_quota.py --raw     # 原始 API 响应

参考: https://github.com/jlcodes99/vscode-antigravity-cockpit
"""

import argparse
import json
import os
import platform
import sqlite3
import struct
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

import requests

# =============================================================================
# OAuth 配置 (来自 vscode-antigravity-cockpit)
# =============================================================================

OAUTH_CLIENT_ID = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
OAUTH_CLIENT_SECRET = "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"
OAUTH_TOKEN_URL = "https://oauth2.googleapis.com/token"
USERINFO_URL = "https://www.googleapis.com/oauth2/v2/userinfo"

# Cloud Code API
CLOUDCODE_BASE_URL = "https://cloudcode-pa.googleapis.com"
FETCH_MODELS_ENDPOINT = "/v1internal:fetchAvailableModels"
LOAD_CODE_ASSIST_ENDPOINT = "/v1internal:loadCodeAssist"

# state.vscdb 中的 key
STATE_KEY = "jetskiStateSync.agentManagerInitState"


# =============================================================================
# Protobuf 解析器 (简化版，仅解析 OAuth token 字段)
# =============================================================================


def read_varint(data: bytes, offset: int) -> tuple[int, int]:
    """读取 varint 编码的整数"""
    result = 0
    shift = 0
    pos = offset
    while pos < len(data):
        byte = data[pos]
        result |= (byte & 0x7F) << shift
        pos += 1
        if (byte & 0x80) == 0:
            return result, pos
        shift += 7
    raise ValueError("Incomplete varint")


def skip_field(data: bytes, offset: int, wire_type: int) -> int:
    """跳过 protobuf 字段"""
    if wire_type == 0:  # Varint
        _, new_offset = read_varint(data, offset)
        return new_offset
    elif wire_type == 1:  # 64-bit
        return offset + 8
    elif wire_type == 2:  # Length-delimited
        length, content_offset = read_varint(data, offset)
        return content_offset + length
    elif wire_type == 5:  # 32-bit
        return offset + 4
    else:
        raise ValueError(f"Unknown wire type: {wire_type}")


def find_field(data: bytes, target_field: int) -> bytes | None:
    """在 protobuf 数据中查找指定字段"""
    offset = 0
    while offset < len(data):
        try:
            tag, new_offset = read_varint(data, offset)
        except ValueError:
            break
        wire_type = tag & 7
        field_num = tag >> 3
        if field_num == target_field and wire_type == 2:
            length, content_offset = read_varint(data, new_offset)
            return data[content_offset : content_offset + length]
        offset = skip_field(data, new_offset, wire_type)
    return None


def parse_timestamp(data: bytes) -> int | None:
    """解析 protobuf timestamp"""
    offset = 0
    while offset < len(data):
        tag, new_offset = read_varint(data, offset)
        wire_type = tag & 7
        field_num = tag >> 3
        offset = new_offset
        if field_num == 1 and wire_type == 0:
            seconds, _ = read_varint(data, offset)
            return seconds
        offset = skip_field(data, offset, wire_type)
    return None


def parse_oauth_token_info(data: bytes) -> dict[str, Any]:
    """解析 OAuth token 信息"""
    offset = 0
    info: dict[str, Any] = {}

    while offset < len(data):
        tag, new_offset = read_varint(data, offset)
        wire_type = tag & 7
        field_num = tag >> 3
        offset = new_offset

        if wire_type == 2:
            length, content_offset = read_varint(data, offset)
            value = data[content_offset : content_offset + length]
            offset = content_offset + length

            if field_num == 1:
                info["access_token"] = value.decode("utf-8")
            elif field_num == 2:
                info["token_type"] = value.decode("utf-8")
            elif field_num == 3:
                info["refresh_token"] = value.decode("utf-8")
            elif field_num == 4:
                info["expiry_seconds"] = parse_timestamp(value)
            continue
        offset = skip_field(data, offset, wire_type)

    return info


# =============================================================================
# 本地凭证读取
# =============================================================================


def get_state_db_path() -> Path:
    """获取 Antigravity state.vscdb 路径"""
    system = platform.system()
    home = Path.home()

    if system == "Darwin":
        return home / "Library/Application Support/Antigravity/User/globalStorage/state.vscdb"
    elif system == "Windows":
        appdata = os.environ.get("APPDATA", str(home / "AppData/Roaming"))
        return Path(appdata) / "Antigravity/User/globalStorage/state.vscdb"
    else:  # Linux
        return home / ".config/Antigravity/User/globalStorage/state.vscdb"


def read_local_token_info() -> dict[str, Any]:
    """从本地 state.vscdb 读取 OAuth token 信息"""
    db_path = get_state_db_path()

    if not db_path.exists():
        raise FileNotFoundError(f"Antigravity 数据库不存在: {db_path}\n请确保已安装并登录 Antigravity 客户端。")

    # 读取数据库
    conn = sqlite3.connect(str(db_path))
    try:
        cursor = conn.execute("SELECT value FROM ItemTable WHERE key = ?", (STATE_KEY,))
        row = cursor.fetchone()
        if not row or not row[0]:
            raise ValueError(f"未找到登录状态，请确保已登录 Antigravity 客户端。")
        state_value = row[0].strip()
    finally:
        conn.close()

    # 解析 base64 + protobuf
    import base64

    raw = base64.b64decode(state_value)

    # OAuth token 在 field 6
    oauth_field = find_field(raw, 6)
    if not oauth_field:
        raise ValueError("未找到 OAuth 凭证，请确保已登录 Antigravity 客户端。")

    return parse_oauth_token_info(oauth_field)


# =============================================================================
# OAuth 刷新
# =============================================================================


def refresh_access_token(refresh_token: str) -> str:
    """使用 refresh_token 获取新的 access_token"""
    response = requests.post(
        OAUTH_TOKEN_URL,
        data={
            "client_id": OAUTH_CLIENT_ID,
            "client_secret": OAUTH_CLIENT_SECRET,
            "refresh_token": refresh_token,
            "grant_type": "refresh_token",
        },
        timeout=10,
    )

    if not response.ok:
        error_text = response.text.lower()
        if "invalid_grant" in error_text:
            raise ValueError("refresh_token 已失效，请重新登录 Antigravity 客户端。")
        raise ValueError(f"Token 刷新失败: {response.status_code} - {response.text}")

    data = response.json()
    return data["access_token"]


def get_user_email(access_token: str) -> str:
    """获取当前登录用户的邮箱"""
    response = requests.get(
        USERINFO_URL,
        headers={"Authorization": f"Bearer {access_token}"},
        timeout=10,
    )
    if not response.ok:
        raise ValueError(f"获取用户信息失败: {response.status_code}")
    return response.json().get("email", "Unknown")


# =============================================================================
# Cloud Code API
# =============================================================================


def load_project_info(access_token: str) -> dict[str, Any]:
    """加载项目信息"""
    response = requests.post(
        f"{CLOUDCODE_BASE_URL}{LOAD_CODE_ASSIST_ENDPOINT}",
        headers={
            "Authorization": f"Bearer {access_token}",
            "Content-Type": "application/json",
            "User-Agent": "antigravity-quota-script",
        },
        json={
            "metadata": {
                "ideType": "ANTIGRAVITY",
                "platform": "PLATFORM_UNSPECIFIED",
                "pluginType": "GEMINI",
            }
        },
        timeout=15,
    )

    if response.status_code == 401:
        raise ValueError("授权已过期，请重新登录 Antigravity 客户端。")
    if not response.ok:
        raise ValueError(f"加载项目信息失败: {response.status_code} - {response.text}")

    return response.json()


def extract_project_id(data: dict[str, Any]) -> str | None:
    """从 loadCodeAssist 响应中提取 project_id"""
    project = data.get("cloudaicompanionProject")
    if isinstance(project, str) and project:
        return project
    if isinstance(project, dict) and project.get("id"):
        return project["id"]
    return None


def fetch_available_models(access_token: str, project_id: str | None = None) -> dict[str, Any]:
    """获取可用模型及配额信息"""
    payload = {}
    if project_id:
        payload["project"] = project_id

    response = requests.post(
        f"{CLOUDCODE_BASE_URL}{FETCH_MODELS_ENDPOINT}",
        headers={
            "Authorization": f"Bearer {access_token}",
            "Content-Type": "application/json",
            "User-Agent": "antigravity-quota-script",
        },
        json=payload,
        timeout=15,
    )

    if response.status_code == 401:
        raise ValueError("授权已过期，请重新登录 Antigravity 客户端。")
    if response.status_code == 403:
        raise ValueError("访问被拒绝 (403)，可能没有权限访问此 API。")
    if not response.ok:
        raise ValueError(f"获取模型信息失败: {response.status_code} - {response.text}")

    return response.json()


# =============================================================================
# 输出格式化
# =============================================================================


def format_time_until(reset_time_str: str) -> str:
    """格式化剩余时间"""
    try:
        reset_time = datetime.fromisoformat(reset_time_str.replace("Z", "+00:00"))
        now = datetime.now(timezone.utc)
        delta = reset_time - now

        if delta.total_seconds() <= 0:
            return "已重置"

        hours = int(delta.total_seconds() // 3600)
        minutes = int((delta.total_seconds() % 3600) // 60)

        if hours > 0:
            return f"{hours}h {minutes}m"
        return f"{minutes}m"
    except Exception:
        return "Unknown"


def format_percentage(fraction: float) -> str:
    """格式化百分比"""
    return f"{fraction * 100:.1f}%"


def get_status_indicator(fraction: float) -> str:
    """根据配额剩余比例返回状态指示器"""
    if fraction >= 0.5:
        return "🟢"
    elif fraction >= 0.1:
        return "🟡"
    else:
        return "🔴"


def print_quota_table(models: dict[str, Any], email: str) -> None:
    """以表格形式打印配额信息"""
    print(f"\n📊 Antigravity 配额状态")
    print(f"   账号: {email}")
    print("=" * 70)

    if not models:
        print("  暂无可用模型")
        return

    # 按显示名称排序
    sorted_models = sorted(models.items(), key=lambda x: x[1].get("displayName", x[0]))

    # 打印表头
    print(f"{'状态':<4} {'模型名称':<35} {'剩余配额':<12} {'重置时间':<12}")
    print("-" * 70)

    for model_key, info in sorted_models:
        quota_info = info.get("quotaInfo", {})
        remaining = quota_info.get("remainingFraction", 0)
        reset_time = quota_info.get("resetTime", "")

        display_name = info.get("displayName", model_key)
        # 截断过长的名称
        if len(display_name) > 33:
            display_name = display_name[:30] + "..."

        status = get_status_indicator(remaining)
        percentage = format_percentage(remaining)
        time_until = format_time_until(reset_time) if reset_time else "N/A"

        print(f" {status}   {display_name:<35} {percentage:<12} {time_until:<12}")

    print("=" * 70)
    print(f"  共 {len(models)} 个模型\n")


def print_json_output(models: dict[str, Any], email: str) -> None:
    """以 JSON 格式输出配额信息"""
    output = {
        "email": email,
        "timestamp": datetime.now(timezone.utc).isoformat(),
        "models": [],
    }

    for model_key, info in models.items():
        quota_info = info.get("quotaInfo", {})
        output["models"].append({
            "id": model_key,
            "displayName": info.get("displayName", model_key),
            "remainingFraction": quota_info.get("remainingFraction", 0),
            "remainingPercentage": quota_info.get("remainingFraction", 0) * 100,
            "resetTime": quota_info.get("resetTime"),
            "supportsImages": info.get("supportsImages", False),
            "supportsVideo": info.get("supportsVideo", False),
            "supportsThinking": info.get("supportsThinking", False),
        })

    print(json.dumps(output, indent=2, ensure_ascii=False))


# =============================================================================
# 主函数
# =============================================================================


def main():
    parser = argparse.ArgumentParser(
        description="查询 Antigravity 模型配额状态",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
示例:
    %(prog)s              # 表格输出
    %(prog)s --json       # JSON 输出
    %(prog)s --raw        # 原始 API 响应
        """,
    )
    parser.add_argument("--json", action="store_true", help="以 JSON 格式输出")
    parser.add_argument("--raw", action="store_true", help="输出原始 API 响应")
    parser.add_argument("--verbose", "-v", action="store_true", help="显示详细日志")
    args = parser.parse_args()

    try:
        # 1. 读取本地凭证
        if args.verbose:
            print("📖 读取本地 Antigravity 凭证...")
        token_info = read_local_token_info()

        refresh_token = token_info.get("refresh_token")
        if not refresh_token:
            print("❌ 未找到 refresh_token，请确保已登录 Antigravity 客户端。", file=sys.stderr)
            sys.exit(1)

        # 2. 刷新 access_token
        if args.verbose:
            print("🔄 刷新 access_token...")
        access_token = refresh_access_token(refresh_token)

        # 3. 获取用户邮箱
        email = get_user_email(access_token)
        if args.verbose:
            print(f"👤 当前账号: {email}")

        # 4. 加载项目信息
        if args.verbose:
            print("📦 加载项目信息...")
        project_info = load_project_info(access_token)
        project_id = extract_project_id(project_info)
        if args.verbose and project_id:
            print(f"📁 Project ID: {project_id}")

        # 5. 获取配额数据
        if args.verbose:
            print("📊 获取配额数据...")
        models_data = fetch_available_models(access_token, project_id)

        # 6. 输出结果
        models = models_data.get("models", {})

        if args.raw:
            print(json.dumps(models_data, indent=2, ensure_ascii=False))
        elif args.json:
            print_json_output(models, email)
        else:
            print_quota_table(models, email)

    except FileNotFoundError as e:
        print(f"❌ {e}", file=sys.stderr)
        sys.exit(1)
    except ValueError as e:
        print(f"❌ {e}", file=sys.stderr)
        sys.exit(1)
    except requests.RequestException as e:
        print(f"❌ 网络请求失败: {e}", file=sys.stderr)
        sys.exit(1)
    except Exception as e:
        print(f"❌ 未知错误: {e}", file=sys.stderr)
        if args.verbose:
            import traceback
            traceback.print_exc()
        sys.exit(1)


if __name__ == "__main__":
    main()
