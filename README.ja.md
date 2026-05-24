# Nginx Request Attribution

🌐 [English](README.md) | [繁體中文](README.zh-TW.md) | **日本語**

軽量な Nginx アクセスログ分析ツール。統計ダッシュボードとリアルタイム監視機能を提供します。

## スクリーンショット

| Dark Mode | Light Mode |
|:-:|:-:|
| ![Dark Mode](docs/screenshot-dark.png) | ![Light Mode](docs/screenshot-light.png) |

## 特徴

- 🚀 **シングルバイナリ** - Go でコンパイルされた単一実行ファイル、追加ランタイム不要
- 📊 **内蔵 Web GUI** - 統計ダッシュボードがバイナリに組み込まれており、追加のフロントエンドデプロイ不要
- 🔍 **多次元フィルタリング** - IP、パス、ドメイン、クエリパラメータ、OS、ブラウザ、ステータスコード等でフィルタリング可能
- 🔑 **キーワード追跡** - 設定したキーワードの出現回数を自動追跡
- 📡 **リアルタイム監視** - ログファイルの新規コンテンツを自動監視
- 🐳 **ワンクリックデプロイ** - Docker / Docker Compose デプロイ対応
- 💾 **SQLite ストレージ** - 軽量データベース、外部データベースサービス不要
- 🌐 **多言語インターフェース** - Web GUI は日本語、英語、繁体字中国語に対応

## クイックスタート

### 方法1：直接実行

```bash
# ビルド
go build -o nginx-req-attr ./cmd/

# 既存ログのインポート
./nginx-req-attr -import /var/log/nginx/access.log

# サービス起動（ログ監視 + Web GUI）
./nginx-req-attr -config config.json
```

### 方法2：Docker デプロイ

```bash
# ワンクリック起動
docker-compose up -d

# または手動 Docker
docker build -t nginx-req-attr .
docker run -d \
  -p 8080:8080 \
  -v /var/log/nginx:/var/log/nginx:ro \
  -v ./data:/app/data \
  nginx-req-attr
```

## 設定

`config.json` を作成：

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

| フィールド | 説明 | デフォルト値 |
|---|---|---|
| `log_path` | Nginx アクセスログのパス | `/var/log/nginx/access.log` |
| `log_format` | ログフォーマット (combined/vhost_combined) | `combined` |
| `listen_addr` | HTTP サーバーリッスンアドレス | `:8080` |
| `db_path` | SQLite データベースファイルパス | `./data/stats.db` |
| `watch` | リアルタイムログ監視の有効化 | `true` |
| `keywords` | 追跡するキーワードリスト | `[]` |
| `input_mode` | 入力モード (`file`/`syslog`/`both`) | `file` |
| `syslog_addr` | Syslog リッスンアドレス | `:1514` |
| `syslog_proto` | Syslog プロトコル (`udp`/`tcp`/`both`) | `udp` |

### 入力モード

- **`file`** — fsnotify イベント駆動でログファイルを監視（デフォルト、高効率、nginx 設定変更不要）
- **`syslog`** — syslog レシーバーを起動し、ネットワーク経由で nginx ログを受信（マルチインスタンス集約に最適）
- **`both`** — ファイル監視と syslog 受信を同時使用

#### Syslog モード設定例

Nginx 設定に追加：
```nginx
access_log syslog:server=127.0.0.1:1514,facility=local7,tag=nginx combined;
```

## API エンドポイント

### GET /api/stats

統計サマリーを取得。以下のクエリパラメータでフィルタリング可能：

| パラメータ | 説明 |
|---|---|
| `start` | 開始日 (YYYY-MM-DD) |
| `end` | 終了日 (YYYY-MM-DD) |
| `ip` | IP アドレス (あいまい検索) |
| `path` | パス (あいまい検索) |
| `domain` | ドメイン (あいまい検索) |
| `query` | クエリ文字列 (あいまい検索) |
| `method` | HTTP メソッド |
| `status` | HTTP ステータスコード |
| `os` | オペレーティングシステム |
| `browser` | ブラウザ |
| `keyword` | キーワード |

**レスポンス例：**
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

リクエスト一覧を取得（ページネーション対応）、追加パラメータ：

| パラメータ | 説明 |
|---|---|
| `limit` | ページあたりの件数 (デフォルト 100) |
| `offset` | オフセット |

## 対応ログフォーマット

### Combined (デフォルト)
```
$remote_addr - $remote_user [$time_local] "$request" $status $body_bytes_sent "$http_referer" "$http_user_agent"
```

### Virtual Host Combined
```
$host $remote_addr - $remote_user [$time_local] "$request" $status $body_bytes_sent "$http_referer" "$http_user_agent"
```

## 開発

```bash
# テスト実行
go test ./...

# ビルド
go build -o nginx-req-attr ./cmd/
```

## ライセンス

MIT License
