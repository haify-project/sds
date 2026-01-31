#!/bin/bash
set -e

echo "Installing SDS Controller..."

# Create directories
sudo mkdir -p /opt/sds/bin
sudo mkdir -p /etc/sds
sudo mkdir -p /var/log/sds

# Build binaries
echo "Building binaries..."
make build

# Install binaries
echo "Installing binaries..."
sudo cp bin/sds-controller /opt/sds/bin/
sudo cp bin/sds-cli /usr/local/bin/

# Install config
if [ ! -f /etc/sds/controller.toml ]; then
    sudo cp configs/controller.toml.example /etc/sds/controller.toml
    echo "Config installed to /etc/sds/controller.toml"
    echo "Please edit the configuration before starting the service"
else
    echo "Config already exists at /etc/sds/controller.toml"
fi

# Install systemd service
sudo cp configs/sds-controller.service /etc/systemd/system/
sudo systemctl daemon-reload

echo "Installation complete!"
echo ""
echo "Next steps:"
echo "  1. Edit /etc/sds/controller.toml"
echo "  2. Start the service: sudo systemctl start sds-controller"
echo "  3. Enable the service: sudo systemctl enable sds-controller"
echo "  4. Check status: sudo systemctl status sds-controller"
