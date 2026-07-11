---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Generator
spec_version: 1.26.0622.1
---

# Generator

_1 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/generators/activity`](#get-generators-activity) — Get All Generators' Activity

## GET `/generators/activity`

**Get All Generators' Activity**

Daily activity summary for a generator within a requested 24-hour day (00:00:00 to 23:59:59 of the selected date).

Generator activity may span across multiple days and is defined by ignition on/off events. For clarity, all timestamps and durations are clamped to the boundaries of the requested day (00:00:00 to 23:59:59).

<strong>Data is synced daily. As a result, current-day data may be incomplete or slightly outdated until the next sync run is completed.</strong>


### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[registration]` | optional | string | Filter vehicle registration, case insensitive, can be partial match | "GRG123" |
| `filter[date]` | optional | `date-only` | This will filter results for the given date | "2022-01-01" |
| `page` | optional | integer | The current page | 1 |
| `limit` | optional | integer | The number of items to display per page | 15 |

### Request Body

_No request body._

### Responses

#### `200` — successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  |  |  |
| `meta` | `pagination` |  |  |  |

#### `401` — Unauthorized access. Authentication is required.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `403` — Access is forbidden. The user does not have permission to access this resource.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `422` — Validation failed for the input parameters.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

#### `500` — Internal server error.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |


---

