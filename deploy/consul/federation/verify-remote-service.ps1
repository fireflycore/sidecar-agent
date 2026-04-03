param(
    [Parameter(Mandatory = $true)]
    [string]$ServiceName,

    [Parameter(Mandatory = $true)]
    [string]$RemoteDatacenter,

    [string]$ConsulHttpAddress = "http://127.0.0.1:8500"
)

$ErrorActionPreference = "Stop"

# 该脚本用于验证当前集群是否已经能查询到远端数据中心的服务实例。
$uri = "$ConsulHttpAddress/v1/health/service/$ServiceName?dc=$RemoteDatacenter"
Write-Host "Querying remote Consul service: $uri"

$response = Invoke-RestMethod -Uri $uri -Method Get

if ($null -eq $response) {
    throw "remote service query returned null response"
}

Write-Host "Remote service query succeeded."
$response | ConvertTo-Json -Depth 8
