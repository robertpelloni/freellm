# Hetzner Deployment Helper Script
# Usage: .\deploy_hetzner.ps1

Write-Host "Rebuilding Linux binary on freellm-linux branch..." -ForegroundColor Cyan
$currentBranch = (git branch --show-current).Trim()

if ($currentBranch -ne "freellm-linux") {
    git checkout freellm-linux
}

$env:GOOS="linux"
$env:GOARCH="amd64"
go build -buildvcs=false -o freellm-linux ./cmd/app/

if ($LASTEXITCODE -ne 0) {
    Write-Error "Failed to compile Linux binary"
    if ($currentBranch -ne "freellm-linux") { git checkout $currentBranch }
    exit 1
}

Write-Host "Uploading binary to Hetzner server..." -ForegroundColor Cyan
scp freellm-linux root@5.161.250.43:/opt/aimoneymachine/freellm.new

if ($LASTEXITCODE -ne 0) {
    Write-Host "Failed to upload. Hetzner server is likely offline or unreachable right now." -ForegroundColor Red
    if ($currentBranch -ne "freellm-linux") { git checkout $currentBranch }
    exit 1
}

Write-Host "Finalizing binary swap and restarting service on server..." -ForegroundColor Cyan
ssh root@5.161.250.43 "mv /opt/aimoneymachine/freellm /opt/aimoneymachine/freellm.bak.`$(date +%s`) && mv /opt/aimoneymachine/freellm.new /opt/aimoneymachine/freellm && chmod +x /opt/aimoneymachine/freellm && systemctl restart aimm-freellm && systemctl status aimm-freellm"

if ($currentBranch -ne "freellm-linux") {
    git checkout $currentBranch
}

Write-Host "Deployment completed successfully!" -ForegroundColor Green
