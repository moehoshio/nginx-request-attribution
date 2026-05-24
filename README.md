# Nginx Request Attribution

一個輕量級的 Nginx 存取日誌分析工具，提供統計報表和即時監控功能。

A lightweight Nginx access log analytics tool with statistics dashboard and real-time monitoring.

## 截圖預覽 Screenshots

| Dark Mode | Light Mode |
|:-:|:-:|
| ![Dark Mode](docs/screenshot-dark.png) | ![Light Mode](docs/screenshot-light.png) |

## 特點 Features

- 🚀 **單一二進位檔** - Go 編譯為單一可執行檔，無需額外 runtime
- 📊 **內建 Web GUI** - 統計報表直接嵌入二進位檔中，無需額外前端部署
- 🔍 **多維度篩選** - 支援按 IP、路徑、域名、查詢參數、OS、瀏覽器、狀態碼等篩選
- 🔑 **關鍵詞追蹤** - 自動追蹤配置的關鍵詞出現次數
- 📡 **即時監控** - 自動監控日誌檔案新增內容
- 🐳 **一鍵部署** - 支援 Docker / Docker Compose 部署
- 💾 **SQLite 儲存** - 輕量級資料庫，無需外部資料庫服務

## 快速開始 Quick Start

### 方式一：直接執行 Direct Run

```bash
# 編譯
go build -o nginx-req-attr ./cmd/

# 匯入既有日誌
./nginx-req-attr -import /var/log/nginx/access.log

# 啟動服務（監控日誌 + Web GUI）
./nginx-req-attr -config config.json
```

### 方式二：Docker 部署 Docker Deploy

```bash
# 一鍵啟動
docker-compose up -d

# 或手動 Docker
docker build -t nginx-req-attr .
docker run -d \
  -p 8080:8080 \
  -v /var/log/nginx:/var/log/nginx:ro \
  -v ./data:/app/data \
  nginx-req-attr
```

## 配置 Configuration

建立 `config.json`：

```json
{
  "log_path": "/var/log/nginx/access.log",
  "log_format": "combined",
  "listen_addr": ":8080",
  "db_path": "./data/stats.db",
  "watch": true,
  "keywords": ["login", "admin", "api", "search"],
  "input_mode": "file",
  "syslog_addr": ":1514",
  "syslog_proto": "udp"
}
```

| 欄位 Field | 說明 Description | 預設值 Default |
|---|---|---|
| `log_path` | Nginx 存取日誌路徑 | `/var/log/nginx/access.log` |
| `log_format` | 日誌格式 (combined/vhost_combined) | `combined` |
| `listen_addr` | HTTP 服務監聯地址 | `:8080` |
| `db_path` | SQLite 資料庫檔案路徑 | `./data/stats.db` |
| `watch` | 是否即時監控日誌 | `true` |
| `keywords` | 要追蹤的關鍵詞列表 | `[]` |
| `input_mode` | 輸入模式 Input mode (`file`/`syslog`/`both`) | `file` |
| `syslog_addr` | Syslog 監聽地址 | `:1514` |
| `syslog_proto` | Syslog 協議 (`udp`/`tcp`/`both`) | `udp` |

### 輸入模式 Input Modes

- **`file`** — 使用 fsnotify 事件驅動監控日誌檔案（預設，高效率，無需修改 nginx 配置）
- **`syslog`** — 啟動 syslog 接收器，透過網路接收 nginx 日誌（適合多實例匯聚）
- **`both`** — 同時使用檔案監控和 syslog 接收

#### Syslog 模式配置範例

Nginx 配置加入：
```nginx
access_log syslog:server=127.0.0.1:1514,facility=local7,tag=nginx combined;
```

## API 介面 API Endpoints

### GET /api/stats

取得統計摘要，支援以下查詢參數篩選：

| 參數 Parameter | 說明 Description |
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

**回應範例 Response Example:**
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

| 參數 Parameter | 說明 Description |
|---|---|
| `limit` | 每頁筆數 (預設 100) |
| `offset` | 偏移量 |

## 支援的日誌格式 Supported Log Formats

### Combined (預設)
```
$remote_addr - $remote_user [$time_local] "$request" $status $body_bytes_sent "$http_referer" "$http_user_agent"
```

### Virtual Host Combined
```
$host $remote_addr - $remote_user [$time_local] "$request" $status $body_bytes_sent "$http_referer" "$http_user_agent"
```

## 開發 Development

```bash
# 執行測試
go test ./...

# 編譯
go build -o nginx-req-attr ./cmd/
```

## 授權 License

MIT License
