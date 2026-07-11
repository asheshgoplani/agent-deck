---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Car Manufacturers
spec_version: 1.26.0622.1
---

# Car Manufacturers

_1 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/manufacturer/customers`](#get-manufacturer-customers) — Get All Customers' Details

## GET `/manufacturer/customers`

**Get All Customers' Details**

Retrieves customer information for authorised vehicle manufacturers.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[vehicle_id]` | optional | integer | Optional filter to retrieve by vehicle ID. | 12345 |
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[company_name]` | optional | string | Filter by company name, case insensitive, can be partial match | Toyota |
| `filter[chassis_number]` | optional | `chassis-number` | Filter by chassis number, case insensitive, can be partial match |  |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Customer details for authorised vehicle manufacturers

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object | **required** | This array returns the list of customer entries |  |
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

