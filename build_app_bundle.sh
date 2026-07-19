#!/bin/bash
set -e

echo "🔨 Building Go binaries..."
go build -o bin/agentwatch ./cmd/agentwatch
go build -o bin/agentwatchd ./cmd/agentwatchd

echo "🔨 Building Swift app..."
cd apps
swift build -c release
cd ..

echo "📦 Packaging AgentWatch.app..."
APP_DIR="AgentWatch.app"
rm -rf "$APP_DIR"
mkdir -p "$APP_DIR/Contents/MacOS"

# Copy binaries
cp apps/.build/release/AgentWatch "$APP_DIR/Contents/MacOS/"
cp bin/agentwatchd "$APP_DIR/Contents/MacOS/"

# Create Info.plist
cat <<EOF > "$APP_DIR/Contents/Info.plist"
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleExecutable</key>
    <string>AgentWatch</string>
    <key>CFBundleIdentifier</key>
    <string>com.agentwatch.AgentWatch</string>
    <key>CFBundleName</key>
    <string>AgentWatch</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleShortVersionString</key>
    <string>1.0</string>
    <key>LSMinimumSystemVersion</key>
    <string>13.0</string>
    <key>LSUIElement</key>
    <true/>
</dict>
</plist>
EOF

echo "✅ AgentWatch.app built successfully at: $(pwd)/AgentWatch.app"
echo ""
echo "To make it run on startup:"
echo "1. Move AgentWatch.app to your /Applications folder."
echo "2. Open System Settings -> General -> Login Items."
echo "3. Add AgentWatch.app to your Login Items."
