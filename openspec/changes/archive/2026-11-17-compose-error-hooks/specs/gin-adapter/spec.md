# Spec Delta

## ADDED Requirements

### Requirement: Status-only aborts are identifiable in the hooks
When the middleware writes a problem for a status-only abort, the error the hooks receive SHALL match the exported `ginprblm.ErrStatusOnly` with `errors.Is`. gin's own 404 and 405 for an unrouted request SHALL match it too. A recorded error SHALL NOT match it and SHALL reach the hooks even after the body was written. A response with no error status and no recorded error SHALL NOT call the hooks.

#### Scenario: Classifying a status-only abort in a hook
- **Given** a gin engine using the middleware with a hook that records its calls
- **When** a handler calls `<call>`
- **Then** the hook was called `<calls>` times
- **And** `errors.Is(err, ginprblm.ErrStatusOnly)` on the recorded error is `<status_only>` and the problem status is `<status>`

##### Examples: status-only
| call | calls | status_only | status |
|------|-------|-------------|--------|
| `c.AbortWithStatus(401)` | 1 | true | 401 |
| `c.AbortWithStatus(429)` | 1 | true | 429 |
| `c.Status(500)` and no body | 1 | true | 500 |

##### Examples: gin's own aborts
| call | calls | status_only | status |
|------|-------|-------------|--------|
| unrouted `GET` (no handler runs) | 1 | true | 404 |
| wrong method with `HandleMethodNotAllowed` (no handler runs) | 1 | true | 405 |

##### Examples: handler errors
| call | calls | status_only | status |
|------|-------|-------------|--------|
| `c.AbortWithError(401, errors.New("token expired"))` | 1 | false | 401 |
| `c.Error(errors.New("boom"))` | 1 | false | 500 |
| `c.Error(fmt.Errorf("vessel 7: %w", prblm.ErrNotFound))` | 1 | false | 404 |
| `c.String(200, "partial")`, then `c.Error(errors.New("boom"))` (error after body) | 1 | false | 500 |

##### Examples: no problem written
| call | calls | status_only | status |
|------|-------|-------------|--------|
| `c.AbortWithStatus(200)` | 0 | (none) | (none) |
| `c.Status(204)` | 0 | (none) | (none) |
| `c.String(500, "plain")` | 0 | (none) | (none) |
