#!/bin/bash

# Start the daemon in the background
./bin/agentwatchd &
DAEMON_PID=$!

# Start the menu bar app in the background
./apps/.build/release/AgentWatch &
APP_PID=$!

echo "=========================================================="
echo "👀 AgentWatch is now running!"
echo "Check your macOS Menu Bar at the top right of your screen."
echo "You should see an eye icon (or a bolt if something is running)."
echo "=========================================================="
echo ""
echo "Try running a test command in a new terminal tab:"
echo "    cd ~/Documents/Coding/agentwatch"
echo "    ./bin/aw sleep 10"
echo ""
echo "Press Ctrl+C here to stop the daemon and close the menu bar app."

# Cleanup on exit
trap "kill $DAEMON_PID $APP_PID 2>/dev/null; exit" SIGINT SIGTERM EXIT
wait
