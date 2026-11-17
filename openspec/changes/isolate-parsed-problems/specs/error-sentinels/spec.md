# Spec Delta

## ADDED Requirements

### Requirement: A received problem reports ErrBadGateway
A received problem SHALL report `ErrBadGateway` through `errors.Is`, also as a nil pointer, and no other sentinel, whatever status it holds. In a chain, the sentinel order rules SHALL apply to it at its position, as to any error whose `Is` method reports a sentinel.

#### Scenario: errors.Is on a received problem
- **Given** a received problem `R` holding status `<held>`
- **When** `errors.Is(<error>, <target>)` is evaluated
- **Then** the result is `<result>`

##### Examples:
| held | error | target | result |
|------|-------|--------|--------|
| 404 | `R` | `prblm.ErrBadGateway` | true |
| 503 | `fmt.Errorf("op: %w", R)` | `prblm.ErrBadGateway` | true |
| 404 | `R` | `prblm.ErrNotFound` | false |
| 503 | `R` | `prblm.ErrUnavailable` | false |
| 0 | `R` | `prblm.ErrBadGateway` | true |
| (none) | `(*prblm.Received)(nil)` | `prblm.ErrBadGateway` | true |
