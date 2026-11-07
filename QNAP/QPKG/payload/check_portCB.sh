#!/bin/bash


# Function to check if a port is open on localhost using telnet
is_port_open() {
  (echo quit | telnet localhost 8789) >/dev/null 2>&1
  return $?
}

# Function to restart the service
restart_service() {
  /etc/init.d/ChaturbateDVR.sh restart
}


# Main loop to check the port every 30 seconds
while true; do
  if ! is_port_open; then
    echo "Port 8789 is not reachable. Restarting the service..."
    restart_service
  else
    echo "Port 8789 is reachable."
  fi
  sleep 30
done