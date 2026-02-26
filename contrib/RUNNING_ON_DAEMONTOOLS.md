# Running AI Gateway with daemontools

This guide explains how to run AI Gateway using Daniel J. Bernstein's [daemontools](https://cr.yp.to/daemontools.html).

## Why daemontools?

daemontools is a collection of tools for managing UNIX services. It provides:
- Automatic service supervision and restart on failure
- Logging
- Clean process lifecycle management
- Simple, reliable operation

## Setup

### 1. Install daemontools

On Debian/Ubuntu:
```bash
apt-get install daemontools-run
```

On RHEL/CentOS:
```bash
yum install daemontools
```

### 2. Create the service directory

```bash
mkdir -p /service/ai-gateway
```

### 3. Create the run script

Create `/service/ai-gateway/run`:

```bash
#!/bin/sh
exec 2>&1
cd /opt/ai-gateway
exec ./ai-gateway -config /etc/ai-gateway/config.yaml
```

Make it executable:
```bash
chmod +x /service/ai-gateway/run
```

### 4. Create the log directory

```bash
mkdir -p /service/ai-gateway/log
```

Create `/service/ai-gateway/log/run`:

```bash
#!/bin/sh
exec multilog t s100000 n10 /var/log/ai-gateway
```

Make it executable:
```bash
chmod +x /service/ai-gateway/log/run
mkdir -p /var/log/ai-gateway
```

### 5. Create the config directory

```bash
mkdir -p /etc/ai-gateway
cp config.yaml.example /etc/ai-gateway/config.yaml
```

Edit the config file with your settings.

### 6. Copy the binary

```bash
cp ai-gateway /opt/ai-gateway/
```

### 7. Start the service

daemontools will automatically detect and start the service:

```bash
# Check if service is running
svstat /service/ai-gateway

# View logs
tail -f /var/log/ai-gateway/current

# Restart the service
svc -t /service/ai-gateway

# Stop the service
svc -d /service/ai-gateway

# Start the service
svc -u /service/ai-gateway
```

## Troubleshooting

### Service won't start

Check the logs:
```bash
tail /var/log/ai-gateway/current
```

Check service status:
```bash
svstat /service/ai-gateway
```

### View all daemontools services

```bash
ls -la /service/
```

## Security

Consider running as a dedicated user:

```bash
# Create dedicated user
useradd -r -s /sbin/nologin ai-gateway

# Set ownership
chown -R ai-gateway:ai-gateway /opt/ai-gateway /etc/ai-gateway /var/log/ai-gateway
```
