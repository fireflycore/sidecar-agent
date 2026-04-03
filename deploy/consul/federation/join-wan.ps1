param(
    [Parameter(Mandatory = $true)]
    [string]$RemoteWanAddress,

    [string]$ConsulBinary = "consul"
)

$ErrorActionPreference = "Stop"

# 该脚本用于在当前集群的 Consul Server 上执行 WAN Join，
# 让当前数据中心与远端数据中心建立 federation 关系。
Write-Host "Joining remote Consul WAN peer: $RemoteWanAddress"

& $ConsulBinary join -wan $RemoteWanAddress

if ($LASTEXITCODE -ne 0) {
    throw "consul join -wan failed with exit code $LASTEXITCODE"
}

Write-Host "Consul WAN join completed."
