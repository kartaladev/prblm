# problem-response Specification

## Purpose

Defines how a resolved problem becomes an HTTP response in a framework-neutral way: status consistency, format negotiation, headers, and what happens when the response cannot be written as planned.

## Requirements

### Requirement: Response status equals problem status
The HTTP response status SHALL always equal the problem's `status` member in the body.

#### Scenario: Status consistency
- **Given** a mapper that returns a problem with status `<returned>`
- **When** the problem is written
- **Then** the HTTP status is `<status>` and the body `status` is `<status>`

##### Examples:
| returned | status |
|----------|--------|
| 409 | 409 |
| 404 | 404 |
| (none) | 500 |

### Requirement: Accept negotiation between JSON and XML
The library SHALL choose the format from the request `Accept` header, honoring q-values and specificity. JSON types and wildcards select `application/problem+json`. `application/problem+xml`, `application/xml` and `text/xml` select `application/problem+xml`. Equal preference SHALL select JSON.

#### Scenario: Negotiated format
- **Given** a request with `Accept: <accept>`
- **When** a 404 problem is written
- **Then** the `Content-Type` is `<content_type>`

##### Examples: JSON
| accept | content_type |
|--------|--------------|
| (none) | `application/problem+json` |
| `*/*` | `application/problem+json` |
| `application/json` | `application/problem+json` |
| `application/problem+json` | `application/problem+json` |
| `application/*` | `application/problem+json` |
| `application/xml, application/json` | `application/problem+json` |
| `application/xml;q=0.5, */*` | `application/problem+json` |

##### Examples: XML
| accept | content_type |
|--------|--------------|
| `application/problem+xml` | `application/problem+xml` |
| `application/xml` | `application/problem+xml` |
| `text/xml` | `application/problem+xml` |
| `application/json;q=0.5, application/xml` | `application/problem+xml` |
| `application/json;q=0, application/xml` | `application/problem+xml` |
| `APPLICATION/XML` | `application/problem+xml` |

### Requirement: Unacceptable or malformed Accept falls back to JSON
When no supported format is acceptable, or the `Accept` header is malformed or too large, the library SHALL respond with `application/problem+json` and the problem's status, never 406.

#### Scenario: Fallback to JSON
- **Given** a request with `Accept: <accept>`
- **When** a 404 problem is written
- **Then** the status is 404 and the `Content-Type` is `application/problem+json`

##### Examples:
| accept |
|--------|
| `image/png` |
| `application/json;q=0, application/xml;q=0` |
| `;;;,,,` |
| `application/xml;q=abc` |
| `application/xml;q=2` |
| an 8 KiB header of repeated `text/html,` |

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

### Requirement: Problem headers cannot override content headers
A problem's own headers SHALL NOT replace `Content-Type` or `Content-Length`, which the library controls.

#### Scenario: Protected headers
- **Given** a problem with header `<header>`
- **When** the problem is written as JSON
- **Then** the response `<name>` is `<value>`

##### Examples:
| header | name | value |
|--------|------|-------|
| `Content-Type: text/html` | `Content-Type` | `application/problem+json` |
| `Content-Length: 1` | `Content-Length` | the real body length |

### Requirement: Framework-neutral response
The library SHALL turn a request description (method, path, `Accept`) and an error into a complete response (status, headers, body) without writing it, so any framework adapter can deliver it. It SHALL report "nothing to write" for a nil error only.

#### Scenario: Respond without writing
- **Given** a request `<request>` with `Accept: <accept>`
- **When** a response is produced for `<error>`
- **Then** the result is `<result>`

##### Examples:
| request | accept | error | result |
|---------|--------|-------|--------|
| `GET /v1/vessels/7` | (none) | `ErrVesselNotFound` (mapped to 404) | status 404, `application/problem+json`, body with instance `/v1/vessels/7` |
| `GET /v1/vessels/7` | `application/xml` | `ErrVesselNotFound` (mapped to 404) | status 404, `application/problem+xml` |
| `GET /v1/vessels/7` | (none) | `nil` | nothing to write |
| `GET /v1/vessels/7` (context canceled) | (none) | `context.Canceled` | status 500, `application/problem+json` |

### Requirement: Encoding failures fall back to a minimal problem
When encoding the problem fails, the library SHALL respond with a constant, pre-encoded `application/problem+json` 500 problem and report the encoding error to the `OnError` hook, wrapped with the exported `ErrEncoding`. No partial body SHALL be sent.

#### Scenario: Encoding fails
- **Given** a request with `Accept: <accept>` and a mapper that adds extension `<extension>`
- **When** the problem is written
- **Then** the status is 500, the `Content-Type` is `application/problem+json` and the body is `{"title":"Internal Server Error","status":500}`
- **And** the hook received an error wrapping `ErrEncoding`

##### Examples:
| accept | extension |
|--------|-----------|
| `application/json` | `bad` holding a `chan int` |
| `application/xml` | `meta` holding a map with key `file.name` |
| `application/json` | `ratio` holding `math.Inf(1)` |

### Requirement: Responses already started are left alone
When the response has already started (status or body sent) before the problem is written, the library SHALL NOT write anything more and SHALL still report the error to the `OnError` hook.

#### Scenario: Handler wrote before failing
- **Given** a handler that `<wrote>` and then returns `ErrVesselNotFound`
- **When** the error is handled
- **Then** the client receives `<received>`
- **And** the hook received `ErrVesselNotFound`

##### Examples:
| wrote | received |
|-------|----------|
| wrote status 200 and body `{"partial":true}` | status 200 and body `{"partial":true}` only |
| wrote status 202 and no body | status 202 and an empty body |
| flushed headers with status 200 | status 200 and an empty body |
| set a header but wrote nothing | a 404 problem |
