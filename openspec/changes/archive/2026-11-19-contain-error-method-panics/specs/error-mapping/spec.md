# Spec Delta

## ADDED Requirements

### Requirement: Error values with panicking methods never break resolution
An error whose own methods panic while the handler resolves it SHALL NOT crash the request; this covers an `Is` or `As` method, and an `As` method reporting a match with an empty target. The resolution step that met the panic SHALL count as no match, each panic SHALL be reported to the hooks as a panic error, and resolution SHALL continue, ending in the safe 500 problem when nothing else matches.

#### Scenario: Error methods panic
- **Given** a handler with `<setup>` and a recording hook
- **When** the handler writes `<error>`
- **Then** the response status is `<status>` and the body is the problem `<body>`
- **And** the body does not contain `boom`
- **And** the hook received `<hook>`

##### Examples: zero configuration
| setup | error | status | body | hook |
|-------|-------|--------|------|------|
| zero configuration | an error whose `As` method panics with `"boom"` | 500 | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | one or more panic errors with value `"boom"`, then the error |
| zero configuration | an error whose `Is` method panics with `"boom"` | 500 | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | one or more panic errors with value `"boom"`, then the error |
| zero configuration | a nil pointer to an error type whose `Is` method has a value receiver | 500 | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | one or more panic errors, then the error |

##### Examples: the status-hint step
| setup | error | status | body | hook |
|-------|-------|--------|------|------|
| built-ins disabled | an error whose `As` method panics with `"boom"` | 500 | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | one or more panic errors with value `"boom"`, then the error |
| built-ins disabled | an error whose `As` method reports a match and leaves its target empty | 500 | `{"title":"Internal Server Error","status":500,"instance":"/v1/vessels/7"}` | one or more panic errors, then the error |

##### Examples: another step still matches
| setup | error | status | body | hook |
|-------|-------|--------|------|------|
| consumer mapper `prblm.Status(ErrX, 409)` | `errors.Join(ErrX, an error whose As method panics with "boom")` | 409 | `{"title":"Conflict","status":409,"instance":"/v1/vessels/7"}` | one or more panic errors with value `"boom"`, then the error |
| zero configuration | `prblm.WithStatus(an error whose Is method panics with "boom", 404)` | 404 | `{"title":"Not Found","status":404,"instance":"/v1/vessels/7"}` | one or more panic errors with value `"boom"`, then the error |

#### Scenario: Error methods panic without a hook
- **Given** a handler with `<setup>` and no hook
- **When** the handler writes `<error>`
- **Then** the response status is `<status>`
- **And** nothing panics out of the handler

##### Examples:
| setup | error | status |
|-------|-------|--------|
| zero configuration | an error whose `As` method panics with `"boom"` | 500 |
| built-ins disabled | an error whose `As` method panics with `"boom"` | 500 |
