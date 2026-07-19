#!/bin/bash
set -e

echo "🚀 Starting AgentWatch installation..."

# 1. Run the build script to compile and bundle the app
./build_app_bundle.sh

# 2. Copy the App bundle to /Applications
echo "📦 Installing AgentWatch.app to /Applications..."
if [ -d "/Applications/AgentWatch.app" ]; then
    echo "Removing existing AgentWatch.app..."
    rm -rf "/Applications/AgentWatch.app"
fi
cp -R AgentWatch.app /Applications/

# 3. Determine the best location for the CLI wrapper
CLI_DEST=""
if [ -d "/opt/homebrew/bin" ] && [ -w "/opt/homebrew/bin" ]; then
    CLI_DEST="/opt/homebrew/bin/agentwatch"
elif [ -d "/usr/local/bin" ]; then
    CLI_DEST="/usr/local/bin/agentwatch"
else
    mkdir -p "$HOME/bin"
    CLI_DEST="$HOME/bin/agentwatch"
fi

echo "🔗 Installing agentwatch CLI wrapper to $CLI_DEST..."
if [ -w "$(dirname "$CLI_DEST")" ]; then
    cp bin/agentwatch "$CLI_DEST"
else
    echo "Elevated permissions required to write to $(dirname "$CLI_DEST")"
    sudo cp bin/agentwatch "$CLI_DEST"
fi

echo ""
echo "🎉 AgentWatch has been installed successfully!"
echo "--------------------------------------------------"
echo "💻 CLI tool: $CLI_DEST"
echo "🖥️  macOS App: /Applications/AgentWatch.app"
echo "--------------------------------------------------"
echo ""
echo "To get started:"
echo "1. Launch the app from your terminal: open /Applications/AgentWatch.app"
echo "   (This starts the menu bar app & background daemon)"
echo "2. Try running the CLI wrapper from any directory: agentwatch sleep 5"
echo "3. To run automatically on login, add AgentWatch.app to:"
echo "   System Settings -> General -> Login Items"
