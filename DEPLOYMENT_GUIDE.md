# Deployment Guide - Production Hosting

Deploy your Moodle MCP Server to the cloud for 24/7 access by ChatGPT, Gemini, and other AI models.

## Quick Decision Guide

| Platform | Difficulty | Cost | Best For |
|----------|-----------|------|----------|
| **Google Cloud Run** | Easy | Free tier available | Gemini integration, quick setup |
| **Heroku** | Easy | $5-7/month | Simple deployment, quick start |
| **AWS Lambda** | Medium | Pay-as-you-go | High traffic, cost-efficient |
| **DigitalOcean** | Medium | $5-12/month | Simple VPS, full control |
| **Docker + Home Server** | Medium | $0 | Self-hosted, no cloud costs |

---

## Option 1: Google Cloud Run (Easiest)

### Step 1: Install Google Cloud CLI

```bash
# macOS
brew install google-cloud-sdk

# Windows: Download from https://cloud.google.com/sdk/docs/install

gcloud init
```

### Step 2: Create a Dockerfile

Create `Dockerfile` in your project root:

```dockerfile
FROM golang:1.22-alpine AS builder
WORKDIR /build
COPY . .
RUN go mod download
RUN go build -o moodle-mcp ./cmd/moodle-mcp/

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /app
COPY --from=builder /build/moodle-mcp .
ENV PORT=8080
EXPOSE 8080
CMD ["./moodle-mcp", "-mode", "rest", "-port", "8080"]
```

### Step 3: Deploy to Cloud Run

```bash
# Create a GCP project (if needed)
gcloud projects create moodle-mcp --set-as-default

# Build and push to Cloud Run
gcloud run deploy moodle-mcp \
  --source . \
  --platform managed \
  --region us-central1 \
  --allow-unauthenticated
```

You'll get a URL like: `https://moodle-mcp-xxxxx.run.app`

### Step 4: Configure Environment Variables

In Cloud Run:
1. Go to your service
2. Edit and expand "Runtime settings"
3. Add environment variables:
   - `MOODLE_URL=https://your-moodle.edu`
   - `MOODLE_USERNAME=your-username`
   - `MOODLE_PASSWORD=your-password`
4. Click **Deploy**

### Step 5: Use with ChatGPT/Gemini

Replace `https://YOUR_URL` in the OpenAPI spec with your Cloud Run URL:
```
https://moodle-mcp-xxxxx.run.app
```

---

## Option 2: Heroku (Simplest)

### Step 1: Install Heroku CLI

```bash
# macOS
brew install heroku

# Windows: Download from https://devcenter.heroku.com/articles/heroku-cli
```

Login:
```bash
heroku login
```

### Step 2: Create Heroku App

```bash
cd ~/fajr
heroku create moodle-mcp
git init
git add .
git commit -m "Initial deployment"
```

### Step 3: Add Buildpack

```bash
heroku buildpacks:add heroku/go
```

### Step 4: Set Environment Variables

```bash
heroku config:set MOODLE_URL="https://your-moodle.edu"
heroku config:set MOODLE_USERNAME="your-username"
heroku config:set MOODLE_PASSWORD="your-password"
heroku config:set REST_API_PORT="5000"
```

### Step 5: Create Procfile

Create `Procfile` in your project root:

```
web: ./bin/moodle-mcp -mode rest -port $PORT
```

### Step 6: Deploy

```bash
git push heroku main
```

You'll get a URL like: `https://moodle-mcp-xxxxx.herokuapp.com`

Monitor with:
```bash
heroku logs --tail
```

---

## Option 3: Docker + Self-Hosted

### Step 1: Build Docker Image

```bash
cd ~/fajr
docker build -t moodle-mcp:latest .
```

### Step 2: Run Locally (Test)

```bash
docker run -d \
  -p 8080:8080 \
  -e MOODLE_URL="https://online.uom.lk" \
  -e MOODLE_USERNAME="your-username" \
  -e MOODLE_PASSWORD="your-password" \
  moodle-mcp:latest
```

Access: `http://localhost:8080/health`

### Step 3: Deploy to VPS (DigitalOcean, Linode, AWS EC2, etc.)

1. **Upload Docker image to Docker Hub:**
   ```bash
   docker tag moodle-mcp:latest yourusername/moodle-mcp:latest
   docker login
   docker push yourusername/moodle-mcp:latest
   ```

2. **On your VPS (Ubuntu 22.04 example):**
   ```bash
   # Install Docker
   curl -fsSL https://get.docker.com -o get-docker.sh
   sudo sh get-docker.sh

   # Run the container
   docker run -d \
     -p 80:8080 \
     --restart unless-stopped \
     -e MOODLE_URL="https://online.uom.lk" \
     -e MOODLE_USERNAME="your-username" \
     -e MOODLE_PASSWORD="your-password" \
     yourusername/moodle-mcp:latest
   ```

3. **Optional: Use nginx as reverse proxy**
   ```bash
   sudo apt-get install nginx
   ```
   Create `/etc/nginx/sites-available/default`:
   ```nginx
   server {
       listen 80;
       server_name your-domain.com;

       location / {
           proxy_pass http://localhost:8080;
           proxy_set_header Host $host;
           proxy_set_header X-Real-IP $remote_addr;
       }
   }
   ```
   ```bash
   sudo systemctl restart nginx
   ```

---

## Option 4: AWS Lambda + API Gateway

### Step 1: Package for Lambda

Create `handler.go`:
```go
package main

import (
	"context"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
)

func HandleRequest(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	// Implement Lambda handler
	// Route requests to your REST API handlers
	return events.APIGatewayProxyResponse{...}, nil
}

func init() {
	lambda.Start(HandleRequest)
}
```

### Step 2: Deploy with AWS SAM

```bash
# Install SAM CLI
brew install aws-sam-cli

# Deploy
sam build
sam deploy --guided
```

This creates an API Gateway endpoint automatically.

---

## Monitoring & Debugging

### Google Cloud Run
```bash
gcloud run logs read moodle-mcp --limit 50
```

### Heroku
```bash
heroku logs --tail
```

### Self-Hosted Docker
```bash
docker logs <container-id>
docker exec -it <container-id> sh
```

---

## Health Check Endpoint

All deployment options support:
```bash
curl https://YOUR_DEPLOYED_URL/health
# Returns: {"status":"ok","authenticated":false}
```

---

## Securing Your Deployment

1. **HTTPS Only**: Use Let's Encrypt (free)
   - Cloud Run: Automatic
   - Heroku: Automatic
   - Docker: Use nginx with Certbot

2. **Authentication**: Add API keys if needed
   - Modify REST server to require `Authorization: Bearer TOKEN`
   - Store in environment variables

3. **Rate Limiting**: Prevent abuse
   - Cloud Run: Built-in rate limiting
   - Docker: Use nginx rate limiting

4. **Never share credentials**: Use environment variables, not hardcoded

---

## Cost Comparison (Monthly)

| Platform | Free Tier | After |
|----------|-----------|-------|
| **Google Cloud Run** | 2M requests/month | $0.15 per 1M requests |
| **Heroku** | No | $5-7 |
| **AWS Lambda** | 1M requests/month | $0.20 per 1M requests |
| **DigitalOcean** | No | $5+ |
| **Self-hosted** | $0 | Electricity only |

**Recommendation for testing:** Start with Google Cloud Run (free tier).
**Recommendation for production:** Heroku or DigitalOcean (fixed cost, reliable).

---

## Next Steps

1. Choose a platform
2. Deploy following the steps above
3. Test your API with:
   ```bash
   curl https://YOUR_DEPLOYED_URL/api/docs
   ```
4. Update your ChatGPT/Gemini OpenAPI spec with the deployed URL
5. Share with your friends!

---

## claude.ai Custom Connector

Use `-mode http` to serve [claude.ai custom connectors](https://support.claude.com/en/articles/11503834) and other Streamable HTTP clients. Below: four free-tier-friendly hosting recipes.

### Common prerequisites

```bash
# Generate a shared secret you'll paste into claude.ai
export MCP_AUTH_TOKEN=$(openssl rand -hex 32)

# Pick one auth path:
# (a) Pre-issued Moodle token (recommended for production):
export MOODLE_URL=https://your.moodle.example
export MOODLE_TOKEN=...
# (b) Username + password (server fetches a token at boot):
# export MOODLE_URL=...; export MOODLE_USERNAME=...; export MOODLE_PASSWORD=...
```

In claude.ai → Settings → Connectors → Add custom connector:
- **URL:** `https://<your-host>/mcp`
- **Custom header:** `Authorization: Bearer <MCP_AUTH_TOKEN>`

---

### Option 1: Railway (recommended — no cold start, free tier)

```bash
# 1. Install Railway CLI
npm i -g @railway/cli
railway login

# 2. From the repo root:
railway init
railway up

# 3. Set env vars in the Railway dashboard:
#    MCP_AUTH_TOKEN, MOODLE_URL, MOODLE_TOKEN
# 4. Set the "Start Command" override to:
#    ./moodle-mcp -mode http
#    (or use examples/deploy/railway.json — Railway picks it up automatically)
```

Railway's $5/month free credit comfortably covers a single-user MCP endpoint.

### Option 2: Smithery (MCP-specific registry)

```bash
# 1. Install Smithery CLI
npm i -g @smithery/cli

# 2. From repo root (smithery.yaml is in examples/deploy/):
cp examples/deploy/smithery.yaml .

# 3. Deploy
smithery deploy

# 4. Configure secrets in the Smithery dashboard:
#    MCP_AUTH_TOKEN, MOODLE_URL, MOODLE_TOKEN
```

### Option 3: Google Cloud Run (scale-to-zero, generous free tier)

```bash
gcloud run deploy moodle-mcp \
  --source . \
  --region us-central1 \
  --allow-unauthenticated \
  --port 8080 \
  --command "./moodle-mcp" \
  --args "-mode,http" \
  --set-env-vars "MCP_AUTH_TOKEN=...,MOODLE_URL=...,MOODLE_TOKEN=..."
```

`--allow-unauthenticated` exposes the URL publicly — the bearer middleware enforces access. Cloud Run scales to zero between requests; the first call after idle takes ~1-2 s to cold-start.

### Option 4: Fly.io

```bash
flyctl launch --no-deploy
# In the generated fly.toml, override the Dockerfile CMD to switch to http mode.
# The Dockerfile uses ENTRYPOINT ["./moodle-mcp"], so [processes] entries are
# args appended to that entrypoint:
#   [processes]
#   app = "-mode http"
#   [env]
#   PORT = "8080"
flyctl secrets set MCP_AUTH_TOKEN=... MOODLE_URL=... MOODLE_TOKEN=...
flyctl deploy
```

---

### Verifying the deployment

```bash
# Health check (no auth required):
curl https://<your-host>/healthz
# → {"status":"ok","version":"1.2.0","mode":"http"}

# Auth gate:
curl -X POST https://<your-host>/mcp -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
# → 401 Unauthorized

curl -X POST https://<your-host>/mcp \
  -H "Authorization: Bearer $MCP_AUTH_TOKEN" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
# → 200 with the tool list
```

If `/healthz` returns 200 and `/mcp` returns 401 without auth + 200 with auth, paste the URL and bearer token into claude.ai's connector setup.
