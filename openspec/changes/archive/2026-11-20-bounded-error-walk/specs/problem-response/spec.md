# Spec Delta

## MODIFIED Requirements

### Requirement: Response headers
Every problem response SHALL set `Content-Type` to the negotiated media type and add `Accept` to `Vary` (keeping existing values). It SHALL set `Cache-Control: no-store` unless a `Cache-Control` header is already present or the problem carries one. A header the problem marks to replace SHALL replace the value already present, `Cache-Control` included; a replaced `Vary` SHALL still list `Accept`, and `Content-Type` and `Content-Length` stay controlled by the library. A mark whose header was deleted from the problem before it was written SHALL replace nothing: the library's `Vary` and `Cache-Control` defaults SHALL then merge with the values already present by the default rules. A header already present under any spelling of its name, such as `cache-control` written directly into net/http's header map or set with fiber's `DisableHeaderNormalizing`, SHALL count as present under that name for every one of these rules, and the response SHALL carry the merged values under the canonical name alone.

#### Scenario: Headers on the response
- **Given** existing response headers `<existing>` and a problem with headers `<problem_headers>`
- **When** the problem is written
- **Then** the response headers include `<expected>`

##### Examples:
| existing | problem_headers | expected |
|----------|-----------------|----------|
| (none) | (none) | `Vary: Accept`, `Cache-Control: no-store` |
| `Vary: Origin` | (none) | `Vary: Origin`, `Vary: Accept` |
| `Vary: Accept` | (none) | exactly one `Accept` in `Vary` |
| `Cache-Control: private` | (none) | `Cache-Control: private` |
| (none) | `Cache-Control: max-age=60` | `Cache-Control: max-age=60` |
| (none) | `Retry-After: 5` | `Retry-After: 5`, `Cache-Control: no-store` |
| `Cache-Control: public, max-age=600` | `Cache-Control: no-store` (with `WithHeader`) | `Cache-Control: public, max-age=600` |

#### Scenario: Replaced headers win over existing ones
- **Given** a `<adapter>` handler that set `<existing>` before returning an error resolved to a problem built with `<fields>`
- **When** the problem is written
- **Then** the response header `<name>` is exactly `<expected>`

##### Examples: net/http
| adapter | existing | fields | name | expected |
|---------|----------|--------|------|----------|
| net/http | `Cache-Control: public, max-age=600` | `prblm.WithReplacedHeader("Cache-Control", "no-store")` | `Cache-Control` | `no-store` |
| net/http | (none) | `prblm.WithReplacedHeader("Cache-Control", "no-store")` | `Cache-Control` | `no-store` |
| net/http | `Vary: Origin` | `prblm.WithReplacedHeader("Vary", "Cookie")` | `Vary` | `Cookie`, `Accept` |
| net/http | `Link: </old>; rel=x` | `prblm.WithReplacedHeader("Link", "</new>; rel=x")` | `Link` | `</new>; rel=x` |
| net/http | (none) | `prblm.WithReplacedHeader("Content-Type", "text/html")` | `Content-Type` | `application/problem+json` |
| net/http | (none) | `prblm.WithReplacedHeader("Content-Length", "1")` | `Content-Length` | the real body length |

##### Examples: other adapters
| adapter | existing | fields | name | expected |
|---------|----------|--------|------|----------|
| gin | `Cache-Control: public, max-age=600` | `prblm.WithReplacedHeader("Cache-Control", "no-store")` | `Cache-Control` | `no-store` |
| fiber | `Cache-Control: public, max-age=600` | `prblm.WithReplacedHeader("Cache-Control", "no-store")` | `Cache-Control` | `no-store` |
| gin | `Cache-Control: public, max-age=600` | `prblm.WithHeader("Cache-Control", "no-store")` | `Cache-Control` | `public, max-age=600` |
| fiber | `Cache-Control: public, max-age=600` | `prblm.WithHeader("Cache-Control", "no-store")` | `Cache-Control` | `public, max-age=600` |

##### Examples: the response falls back
| adapter | existing | fields | name | expected |
|---------|----------|--------|------|----------|
| net/http | `Cache-Control: public, max-age=600` | `prblm.WithReplacedHeader("Cache-Control", "no-store")`, `prblm.WithExt("bad", make(chan int))` | `Cache-Control` | `public, max-age=600` |

##### Examples: the marked header was deleted
An enricher deletes the marked header from the problem before it is written.

| adapter | existing | fields | name | expected |
|---------|----------|--------|------|----------|
| net/http | `Cache-Control: public, max-age=60` | `prblm.WithReplacedHeader("Cache-Control", "x")` | `Cache-Control` | `public, max-age=60` |
| net/http | `Vary: Origin` | `prblm.WithReplacedHeader("Vary", "x")` | `Vary` | `Origin`, `Accept` |
| net/http | `Link: </old>; rel=x` | `prblm.WithReplacedHeader("Link", "</new>; rel=x")` | `Link` | `</old>; rel=x` |
| net/http | (none) | `prblm.WithReplacedHeader("Cache-Control", "x")` | `Cache-Control` | `no-store` |
| gin | `Cache-Control: public, max-age=60` | `prblm.WithReplacedHeader("Cache-Control", "x")` | `Cache-Control` | `public, max-age=60` |
| gin | `Vary: Origin` | `prblm.WithReplacedHeader("Vary", "x")` | `Vary` | `Origin`, `Accept` |
| fiber | `Cache-Control: public, max-age=60` | `prblm.WithReplacedHeader("Cache-Control", "x")` | `Cache-Control` | `public, max-age=60` |
| fiber | `Vary: Origin` | `prblm.WithReplacedHeader("Vary", "x")` | `Vary` | `Origin`, `Accept` |

#### Scenario: A handler header under a non-canonical key
- **Given** a `<adapter>` handler that set `<existing>` under the key `<key>` (net/http and gin: a direct write to the header map; fiber: `c.Set` with `DisableHeaderNormalizing` on and off) before returning an error resolved to a problem built with `<fields>`
- **When** the problem is written
- **Then** the values sent for `<name>`, under every spelling, are exactly `<expected>`

##### Examples:
| adapter | key | existing | fields | name | expected |
|---------|-----|----------|--------|------|----------|
| net/http | `cache-control` | `public, max-age=600` | `prblm.WithReplacedHeader("Cache-Control", "no-store")` | `Cache-Control` | `no-store` |
| net/http | `cache-control` | `public, max-age=600` | `prblm.WithHeader("Cache-Control", "no-store")` | `Cache-Control` | `public, max-age=600` |
| net/http | `vary` | `Origin` | (none) | `Vary` | `Origin`, `Accept` |
| net/http | `content-type` and `CONTENT-TYPE` | `text/html`, `text/plain` | (none) | `Content-Type` | `application/problem+json` |
| gin | `cache-control` | `public, max-age=600` | `prblm.WithReplacedHeader("Cache-Control", "no-store")` | `Cache-Control` | `no-store` |
| gin | `cache-control` | `public, max-age=600` | `prblm.WithHeader("Cache-Control", "no-store")` | `Cache-Control` | `public, max-age=600` |
| gin | `vary` | `Origin` | (none) | `Vary` | `Origin`, `Accept` |
| fiber | `cache-control` | `public, max-age=600` | `prblm.WithReplacedHeader("Cache-Control", "no-store")` | `Cache-Control` | `no-store` |
| fiber | `cache-control` | `public, max-age=600` | `prblm.WithHeader("Cache-Control", "no-store")` | `Cache-Control` | `public, max-age=600` |
| fiber | `vary` | `Origin` | (none) | `Vary` | `Origin`, `Accept` |
| fiber | `LINK` | `</old>; rel=x` | `prblm.WithReplacedHeader("Link", "</new>; rel=x")` | `Link` | `</new>; rel=x` |

##### Examples: the merge alone (`Response.MergeInto`)
| existing | response header | result |
|----------|-----------------|--------|
| `cache-control: c`, `Cache-Control: a`, `CACHE-CONTROL: b` | `Cache-Control: no-store` | `Cache-Control: a, b, c` (canonical first, then the other keys sorted) |
| `cache-control` with no values | `Cache-Control: no-store` | `Cache-Control: no-store` |
| `x-request-id: r1` | `Content-Type: application/problem+json` | `x-request-id: r1` left as it is |
| `cache control: max-age=5` (not a valid field name) | `Cache-Control: no-store` | `cache control` left as it is, `Cache-Control: no-store` |
