---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: CarWatch
spec_version: 1.26.0622.1
---

# CarWatch

_2 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/carwatch/status`](#get-carwatch-status) — Retrieve CarWatch Status
- [POST `/carwatch`](#post-carwatch) — Activate/Deactivate CarWatch

## GET `/carwatch/status`

**Retrieve CarWatch Status**

This endpoint retrieves the list of vehicles with their CarWatch status, indicating whether CarWatch on the respective vehicle is active or inactive.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[is_active]` | optional | boolean | Filter by status. True for active, false for inactive. | True |
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[vehicle_id]` | optional | integer | Optional filter to retrieve by vehicle ID. | 12345 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `carwatch-status` |  | List of carwatch status. |  |
| `meta` | `pagination` |  |  |  |

**Content-Type:** `application/xml`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `carwatch-status` |  | List of carwatch status. |  |
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

#### `404` — The requested resource was not found.

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

## POST `/carwatch`

**Activate/Deactivate CarWatch**

This endpoint allows you to activate (on) or deactivate (off) CarWatch for a single vehicle.

### Request Body

A JSON payload containing the data required to update carwatch status.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `vehicle_id` | integer | null |  | The id of the vehicle. Required if registration is null. | 654321 |
| `registration` | string | null |  | The registration of the vehicle. Required if vehicle_id is null. | "ABC1234X" |
| `activate` | boolean | **required** | Send true to activate carwatch. Send false to deactivate carwatch. | true |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  | Response message. |  |

**Content-Type:** `application/xml`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  | Response message. |  |

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

#### `404` — The requested resource was not found.

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

