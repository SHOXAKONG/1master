#!/bin/bash
# Run this ONCE on your fresh VPS to set everything up
# Usage: ssh root@your-vps "bash -s" < setup-vps.sh

set -e

echo "🚇 MyTunnel VPS Setup"
echo "====================="

# Update system
apt update && apt upgrade -y

# Install Docker
if ! command -v docker &> /dev/null; then
    echo "📦 Installing Docker..."
    curl -fsSL https://get.docker.com | sh
    systemctl enable docker
    systemctl start docker
    echo "✅ Docker installed"
else
    echo "✅ Docker already installed"
fi

# Install Docker Compose plugin
if ! docker compose version &> /dev/null; then
    echo "📦 Installing Docker Compose..."
    apt install -y docker-compose-plugin
    echo "✅ Docker Compose installed"
else
    echo "✅ Docker Compose already installed"
fi

# Create app directory
mkdir -p ~/mytunnel
cd ~/mytunnel

# Create .env file if it doesn't exist
if [ ! -f .env ]; then
    echo "📝 Creating .env file..."
    read -p "Enter your tunnel domain (e.g., tunnel.example.com): " TUNNEL_DOMAIN
    read -p "Enter your GitHub repo (e.g., user/mytunnel): " GITHUB_REPO
    read -p "Enter your Cloudflare API token: " CF_API_TOKEN
    read -p "Enter your email (for Let's Encrypt): " ACME_EMAIL

    cat > .env << EOF
GITHUB_REPO=${GITHUB_REPO}
TUNNEL_DOMAIN=${TUNNEL_DOMAIN}
CF_API_TOKEN=${CF_API_TOKEN}
ACME_EMAIL=${ACME_EMAIL}
EOF
    echo "✅ .env created"
else
    echo "✅ .env already exists"
fi

# Configure firewall
echo "🔥 Configuring firewall..."
ufw allow 22/tcp    # SSH
ufw allow 80/tcp    # HTTP
ufw allow 443/tcp   # HTTPS
ufw allow 9000/tcp  # Tunnel connections
ufw --force enable
echo "✅ Firewall configured"

echo ""
echo "🎉 VPS setup complete!"
echo ""
echo "Next steps:"
echo "1. Add these GitHub Secrets in your repo settings:"
echo "   - VPS_HOST     = $(curl -s ifconfig.me)"
echo "   - VPS_USER     = $(whoami)"
echo "   - VPS_SSH_KEY  = (your private SSH key)"
echo ""
echo "2. Set up wildcard DNS:"
echo "   *.${TUNNEL_DOMAIN} → $(curl -s ifconfig.me)"
echo ""
echo "3. Push to main branch — GitHub Actions will deploy automatically!"