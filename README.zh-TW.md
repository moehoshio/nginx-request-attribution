# Web Request Attribution

🌐 [English](README.md) | **繁體中文** | [简体中文](README.zh-CN.md) | [日本語](README.ja.md)

一個輕量級的 Web 伺服器（Nginx / Apache）存取日誌分析工具，提供統計報表和即時監控功能，並支援自訂日誌格式。

## 截圖預覽

| Dark Mode | Light Mode |
|:-:|:-:|
| ![Dark Mode](docs/screenshot-dark.png) | ![Light Mode](docs/screenshot-light.png) |

## 特點

- 🚀 **單一二進位檔** - Go 編譯為單一可執行檔，無需額外 runtime
- 📊 **內建 Web GUI** - 統計報表直接嵌入二進位檔中，無需額外前端部署
- 🔍 **多維度篩選** - 支援按 IP、路徑、域名、查詢參數、OS、瀏覽器、狀態碼等篩選
- 🔑 **關鍵詞追蹤** - 自動追蹤配置的關鍵詞出現次數
- 📡 **即時監控** - 自動監控日誌檔案新增內容
- 🐳 **一鍵部署** - 支援 Docker / Docker Compose 部署
- 💾 **SQLite 儲存** - 輕量級資料庫，無需外部資料庫服務
- 🌐 **多語言介面** - Web GUI 支援繁體中文、英文、日文

## 快速開始

### 方式一：從 Release 下載

從 [最新 GitHub Release](https://github.com/moehoshio/web-request-attribution/releases/latest) 下載符合平台的預先編譯二進位檔：

| 平台 | 檔案 |
|---|---|
| Linux x86_64 | `web-req-attr-linux-amd64` |
| Linux ARM64 | `web-req-attr-linux-arm64` |
| macOS Intel | `web-req-attr-darwin-amd64` |
| macOS Apple Silicon | `web-req-attr-darwin-arm64` |
| Windows x86_64 | `web-req-attr-windows-amd64.exe` |

```bash
# Linux/macOS 範例
chmod +x web-req-attr-linux-amd64
./web-req-attr-linux-amd64 -config config.json

# 匯入既有日誌
./web-req-attr-linux-amd64 -import /var/log/nginx/access.log
```

### 方式二：從原始碼編譯

```bash
go build -o web-req-attr ./cmd/
./web-req-attr -config config.json
```

### 方式三：Docker 部署

```bash
# 一鍵啟動
docker-compose up -d

# 或手動 Docker
docker build -t web-req-attr .
docker run -d \
  -p 8080:8080 \
  -v /var/log/nginx:/var/log/nginx:ro \
  -v ./data:/app/data \
  web-req-attr
```

## 配置

建立 `config.json`：

```json
{
  "listen_addr": ":8080",
  "db_path": "./data/stats.db",
  "watch": true,
  "keywords": ["login", "admin", "api", "search"],
  "sources": [
    {
      "name": "nginx-main",
      "type": "file",
      "path": "/var/log/nginx/access.log",
      "read_compressed": false,
      "format": { "engine": "nginx", "preset": "combined" }
    }
  ]
}
```

> 詳細的來源欄位（`type` / `format.engine` / `format.preset` / `format.pattern` / `read_compressed` / `pattern` / `recursive` 等）以及自訂格式變數列表，請參考英文 [README](README.md#configuration) 與 [`docs/TODO.md`](docs/TODO.md)。已支援 **Nginx**、**Apache**（讀取日誌檔）、**自訂格式**、`.gz` 壓縮日誌，以及 **資料夾掃描** (`type: "dir"` + 檔名 glob，自動處理日誌輪替)。


#### Syslog 模式配置範例

Nginx 配置加入：
```nginx
access_log syslog:server=127.0.0.1:1514,facility=local7,tag=nginx combined;
```

## API 介面

### GET /api/stats

取得統計摘要，支援以下查詢參數篩選：

| 參數 | 說明 |
|---|---|
| `start` | 開始日期 (YYYY-MM-DD) |
| `end` | 結束日期 (YYYY-MM-DD) |
| `ip` | IP 位址 (模糊搜尋) |
| `path` | 路徑 (模糊搜尋) |
| `domain` | 域名 (模糊搜尋) |
| `query` | 查詢字串 (模糊搜尋) |
| `method` | HTTP 方法 |
| `status` | HTTP 狀態碼 |
| `os` | 作業系統 |
| `browser` | 瀏覽器 |
| `keyword` | 關鍵詞 |

**回應範例：**
```json
{
  "total_requests": 12345,
  "top_paths": [{"name": "/api/users", "count": 500}],
  "top_ips": [{"name": "192.168.1.1", "count": 300}],
  "top_domains": [{"name": "example.com", "count": 200}],
  "top_os": [{"name": "Windows", "count": 5000}],
  "top_browsers": [{"name": "Chrome", "count": 8000}],
  "top_keywords": [{"name": "api", "count": 1500}],
  "status_codes": [{"name": "200", "count": 10000}],
  "requests_per_day": [{"date": "2023-10-10", "count": 500}]
}
```

### GET /api/requests

取得請求列表（分頁），額外支援：

| 參數 | 說明 |
|---|---|
| `limit` | 每頁筆數 (預設 100) |
| `offset` | 偏移量 |

## 支援的日誌格式

### Combined (預設)
```
$remote_addr - $remote_user [$time_local] "$request" $status $body_bytes_sent "$http_referer" "$http_user_agent"
```

### Virtual Host Combined
```
$host $remote_addr - $remote_user [$time_local] "$request" $status $body_bytes_sent "$http_referer" "$http_user_agent"
```

## 開發

```bash
# 執行測試
go test ./...

# 編譯
go build -o web-req-attr ./cmd/
```

## 授權

MIT License
