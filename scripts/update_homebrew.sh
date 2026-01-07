#!/bin/bash

# Configuration
REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TAP_FILE="${REPO_DIR}/../homebrew-tap/oauth-llm-nexus.rb"

# 1. Get current version
RAW_VERSION=$(git describe --tags --always)
VERSION=$(echo $RAW_VERSION | sed 's/^v//')

echo "🚀 Preparing Homebrew update for version: ${VERSION}"

if [ ! -f "$TAP_FILE" ]; then
    echo "❌ Error: Homebrew formula not found at ${TAP_FILE}"
    exit 1
fi

# 2. Fetch checksums.txt from GitHub Release
echo "🌐 Fetching checksums from GitHub Release..."
CHECKSUMS_URL="https://github.com/pysugar/oauth-llm-nexus/releases/download/v${VERSION}/checksums.txt"
TEMP_FILE=$(mktemp)

if curl -sL --fail -o "$TEMP_FILE" "$CHECKSUMS_URL"; then
    echo "✅ Checksums downloaded successfully."
else
    echo "❌ Error: Failed to download checksums.txt from ${CHECKSUMS_URL}"
    echo "💡 Make sure the GitHub Action 'Release' has finished and uploaded checksums.txt"
    rm -f "$TEMP_FILE"
    exit 1
fi

# Function to extract hash from checksums file
get_hash_from_file() {
    local platform=$1
    grep "nexus-${platform}" "$TEMP_FILE" | cut -d' ' -f1
}

H_DARWIN_AMD64=$(get_hash_from_file "darwin-amd64")
H_DARWIN_ARM64=$(get_hash_from_file "darwin-arm64")
H_LINUX_AMD64=$(get_hash_from_file "linux-amd64")
H_LINUX_ARM64=$(get_hash_from_file "linux-arm64")

if [ -z "$H_DARWIN_AMD64" ] || [ -z "$H_DARWIN_ARM64" ] || [ -z "$H_LINUX_AMD64" ] || [ -z "$H_LINUX_ARM64" ]; then
    echo "❌ Error: Could not find all hashes in checksums.txt"
    cat "$TEMP_FILE"
    rm -f "$TEMP_FILE"
    exit 1
fi

# 3. Update the Homebrew formula
echo "📝 Updating Homebrew formula with new hashes..."
sed -i.bak "s/version \".*\"/version \"${VERSION}\"/" "$TAP_FILE"
sed -i.bak "/nexus-darwin-amd64\"/{n;s/sha256 \".*\"/sha256 \"${H_DARWIN_AMD64}\"/;}" "$TAP_FILE"
sed -i.bak "/nexus-darwin-arm64\"/{n;s/sha256 \".*\"/sha256 \"${H_DARWIN_ARM64}\"/;}" "$TAP_FILE"
sed -i.bak "/nexus-linux-amd64\"/{n;s/sha256 \".*\"/sha256 \"${H_LINUX_AMD64}\"/;}" "$TAP_FILE"
sed -i.bak "/nexus-linux-arm64\"/{n;s/sha256 \".*\"/sha256 \"${H_LINUX_ARM64}\"/;}" "$TAP_FILE"

rm -f "${TAP_FILE}.bak"
rm -f "$TEMP_FILE"

echo "✅ Update complete!"
echo "--------------------------------------------------"
echo "Changes in ${TAP_FILE}:"
git -C "${REPO_DIR}/../homebrew-tap" diff oauth-llm-nexus.rb
echo "--------------------------------------------------"
echo "Next steps:"
echo "1. Verify the diff."
echo "2. Push changes to homebrew-tap."
