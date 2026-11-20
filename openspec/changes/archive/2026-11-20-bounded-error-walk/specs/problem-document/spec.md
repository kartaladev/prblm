# Spec Delta

## ADDED Requirements

### Requirement: Problems can mark headers to replace those already set
A problem SHALL be able to carry a header marked to replace, through an explicit, separately named field option, so the header wins over a value the handler already set on the response. A header added the ordinary way SHALL keep the default merge rules. The mark SHALL survive copying the problem, and SHALL never be serialized into the document.

#### Scenario: Marking a header
- **Given** a problem built with `<fields>`
- **When** the problem is resolved and encoded as JSON
- **Then** its headers are `<headers>`, of which `<replaced>` are marked to replace
- **And** the document has no member named after any header

##### Examples: default
| fields | headers | replaced |
|--------|---------|----------|
| `prblm.WithHeader("Cache-Control", "no-store")` | `Cache-Control: no-store` | (none) |
| (none) | (none) | (none) |

##### Examples: opt-in
| fields | headers | replaced |
|--------|---------|----------|
| `prblm.WithReplacedHeader("Cache-Control", "no-store")` | `Cache-Control: no-store` | `Cache-Control` |
| `prblm.WithReplacedHeader("cache-control", "no-store")` | `Cache-Control: no-store` | `Cache-Control` |
| `prblm.WithReplacedHeader("Link", "</a>; rel=a")`, `prblm.WithReplacedHeader("Link", "</b>; rel=b")` | `Link: </a>; rel=a`, `Link: </b>; rel=b` | `Link` |
| `prblm.WithHeader("Retry-After", "5")`, `prblm.WithReplacedHeader("Cache-Control", "no-store")` | `Retry-After: 5`, `Cache-Control: no-store` | `Cache-Control` |

#### Scenario: Package-level problems keep their marks
- **Given** a package-level problem `<problem>` shared by `<n>` concurrent requests under the race detector
- **When** an enricher on each request adds `WithHeader("X-Request", <id>)` to the resolved copy
- **Then** each response carries its own `X-Request`, the shared problem is unchanged, and `Cache-Control` is `no-store` in every response

##### Examples:
| problem | n |
|---------|---|
| `prblm.New(503, prblm.WithReplacedHeader("Cache-Control", "no-store"))` | 64 |
