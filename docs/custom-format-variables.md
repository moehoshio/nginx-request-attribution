# Custom Format Variable Reference

When using `format.engine: "custom"`, you supply a `pattern` string that
describes your log line layout. The pattern uses **Nginx-style `$variable`
tokens** to indicate where each field appears.

## Supported Variables

| Variable | Maps to | Regex used | Notes |
|---|---|---|---|
| `$remote_addr` | IP | `\S+` | Client IP address |
| `$remote_user` | *(discarded)* | `\S+` | HTTP basic-auth user; captured but not stored |
| `$time_local` | Timestamp | `[^\]]+?` | Format: `02/Jan/2006:15:04:05 -0700` (same as Nginx/Apache default) |
| `$msec` | Timestamp | `\d+(?:\.\d+)?` | Unix epoch seconds (fractional OK, e.g. `1696940136.123`) |
| `$request` | Method, Path, Query, Protocol | `[^"]+` | Full request line, e.g. `GET /path?q=1 HTTP/1.1` |
| `$request_method` | Method | `\S+` | HTTP method alone (`GET`, `POST`, …) |
| `$request_uri` | Path + Query | `\S+` | URI including query string |
| `$uri` | Path + Query | `\S+` | Alias for `$request_uri` |
| `$status` | Status | `\d+` | HTTP response status code |
| `$body_bytes_sent` | BodySize | `\d+\|-` | Response body size in bytes |
| `$bytes_sent` | BodySize | `\d+\|-` | Alias for `$body_bytes_sent` |
| `$http_referer` | Referer | `[^"]*?` | `Referer` header value |
| `$http_user_agent` | UserAgent | `[^"]*?` | `User-Agent` header value |
| `$http_host` | Domain | `\S+` | `Host` header value |
| `$host` | Domain | `\S+` | Alias for `$http_host` |
| `$server_name` | Domain | `\S+` | Alias for `$http_host` |
| `$request_time` | *(discarded)* | `\S+` | Request processing time; captured but not stored |
| `$upstream_response_time` | *(discarded)* | `\S+` | Upstream response time; captured but not stored |

## Unknown Variables

Any `$variable_name` token that is **not** in the table above is accepted
without error. It will be matched non-greedily (`.*?`) and its captured value
is discarded. This allows patterns copied from production configs to work even
when they contain fields that this tool does not use.

## Literal Characters

All non-variable characters in the pattern are treated as literal regex-escaped
text. Common examples:

- Brackets (`[`, `]`) around timestamps
- Quotes (`"`) around request/referer/user-agent fields
- Pipes (`|`) or tabs used as delimiters in custom formats
- Dashes (`-`) as separators

## Examples

### Nginx combined (default)

```text
$remote_addr - $remote_user [$time_local] "$request" $status $body_bytes_sent "$http_referer" "$http_user_agent"
```

### Minimal pipe-delimited

```text
$remote_addr|$status|$request_method|$request_uri
```

### With vhost and timing

```text
$http_host $remote_addr [$time_local] "$request" $status $body_bytes_sent $request_time
```

### Tab-separated with epoch time

```text
$msec	$remote_addr	$request_method	$uri	$status
```

## Configuration

```json
{
  "sources": [
    {
      "name": "my-custom-log",
      "type": "file",
      "path": "/var/log/myapp/access.log",
      "format": {
        "engine": "custom",
        "pattern": "$remote_addr | $time_local | \"$request\" | $status"
      }
    }
  ]
}
```

## Limitations

- **Apache `%`-style tokens** (e.g. `%h %l %u %t \"%r\" %>s %b`) are not yet
  supported in the custom engine. Use `format.engine: "apache"` with a preset
  for standard Apache layouts. See [`TODO.md`](./TODO.md) for tracking.
- Variables like `$request_length`, `$ssl_protocol`, `$ssl_cipher`,
  `$gzip_ratio` are not yet mapped; they will be matched and discarded as
  unknown variables.
- The pattern is anchored at the start of the line (`^`) but not at the end;
  trailing content after the last token is silently ignored.
