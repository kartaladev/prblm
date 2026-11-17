# Spec Delta

## ADDED Requirements

### Requirement: Status-only aborts are identifiable in the hooks
When the middleware writes a problem for a status-only abort, the error the hooks receive SHALL match the exported `ginprblm.ErrStatusOnly` with `errors.Is`. An error the handler recorded SHALL NOT match it, and a response the middleware does not turn into a problem SHALL NOT call the hooks.

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

##### Examples: handler errors
| call | calls | status_only | status |
|------|-------|-------------|--------|
| `c.AbortWithError(401, errors.New("token expired"))` | 1 | false | 401 |
| `c.Error(errors.New("boom"))` | 1 | false | 500 |
| `c.Error(fmt.Errorf("vessel 7: %w", prblm.ErrNotFound))` | 1 | false | 404 |

##### Examples: no problem written
| call | calls | status_only | status |
|------|-------|-------------|--------|
| `c.AbortWithStatus(200)` | 0 | (none) | (none) |
| `c.Status(204)` | 0 | (none) | (none) |
| `c.String(500, "plain")` | 0 | (none) | (none) |
