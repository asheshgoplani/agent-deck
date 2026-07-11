---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Points of interest
spec_version: 1.26.0622.1
---

# Points of interest

_5 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/pois`](#get-pois) — Get all points of interest (POIs)
- [POST `/pois`](#post-pois) — Add a new point of interest (POI)
- [GET `/pois/{poi_id}`](#get-pois-poi-id) — Get a point of interest (POI) by id
- [PUT `/pois/{poi_id}`](#put-pois-poi-id) — Update a point of interest (POI)
- [DELETE `/pois/{poi_id}`](#delete-pois-poi-id) — Delete a point of interest (POI)

## GET `/pois`

**Get all points of interest (POIs)**

Get all/filtered list of points of interest (POIs)

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[name]` | optional | string | Optional filter to retrieve by name. | This is an API alert for Gofence 0724 |
| `filter[description]` | optional | string | Filter by group description, case insensitive, can be partial match | "All active geofences" |
| `filter[subuser_id]` | optional | string | Filter by sub-user ID | 62462fcf-0938-11ec-8c4d-a4bf016cd6b2 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

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

## POST `/pois`

**Add a new point of interest (POI)**

This endpoint creates a point of interest (POI)

### Request Body

The json data that needs to be processed

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `name` | string | **required** | The point of interest's name | "Name for POI" |
| `description` | string |  | The point of interest's description | "Your description" |
| `latitude` | number | **required** | Decimal latitude | 28.858403 |
| `longitude` | number | **required** | Decimal longitude | 28.2945 |
| `geo_radius` | integer | **required** | The point of interest's radius in meters | 300 |
| `colour` | string |  | Hexadecimal colour value | "#334455" |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  |  |  |

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

## GET `/pois/{poi_id}`

**Get a point of interest (POI) by id**

This endpoint gets a single point of interest (POI) by its id

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `poi_id` | **required** | integer | The POI's ID | 12345 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  |  |  |

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

## PUT `/pois/{poi_id}`

**Update a point of interest (POI)**

This endpoint updates a point of interest (POI)

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `poi_id` | **required** | integer | The POI's ID | 12345 |

### Request Body

One or more of the parameters to be updated

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `name` | string |  | The point of interest's name | "Name for POI" |
| `description` | string |  | The point of interest's description | "Your description" |
| `latitude` | number |  | Decimal latitude | 28.858403 |
| `longitude` | number |  | Decimal longitude | 28.2945 |
| `geo_radius` | integer |  | The point of interest's radius in meters | 300 |
| `colour` | string |  | Hexadecimal colour value | "#334455" |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  |  |  |

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

## DELETE `/pois/{poi_id}`

**Delete a point of interest (POI)**

This endpoint deletes a point of interest (POI)

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `poi_id` | **required** | integer | The POI's ID | 12345 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  | Data won't return anything | "{}" |
| `meta` | object |  | The meta object containing a message |  |

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

