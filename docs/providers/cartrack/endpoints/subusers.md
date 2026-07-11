---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Subusers
spec_version: 1.26.0622.1
---

# Subusers

_4 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/subusers`](#get-subusers) — Get all subusers
- [POST `/subusers/apikey/{subuser_id}`](#post-subusers-apikey-subuser-id) — Generate an API Key
- [DELETE `/subusers/apikey/{subuser_id}`](#delete-subusers-apikey-subuser-id) — Revoke an API Key
- [GET `/subusers/login-history`](#get-subusers-login-history) — Get subusers login history

## GET `/subusers`

**Get all subusers**

This endpoint returns all the subusers of your account. This endpoint only applies to admin user.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `page` | optional | integer | The current page | 1 |
| `limit` | optional | integer | The number of items to display per page | 15 |

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

## POST `/subusers/apikey/{subuser_id}`

**Generate an API Key**

This endpoint will generate or regenerate an API key a subuser of your account. This endpoint only applies to admin user.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `subuser_id` | **required** | string | The subuser's ID | 6226624c-f819-11ec-b939-0242ac120002 |

### Request Body

The json data (empty object)

**Content-Type:** `application/json`


_Schema: object_

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

## DELETE `/subusers/apikey/{subuser_id}`

**Revoke an API Key**

This endpoint will revoke the API key of a subuser of your account. This endpoint only applies to admin user.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `subuser_id` | **required** | string | The subuser's ID | 6226624c-f819-11ec-b939-0242ac120002 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | null |  | Data won't return anything |  |
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

## GET `/subusers/login-history`

**Get subusers login history**

This endpoint returns all the subusers of login history. This endpoint only applies to admin user.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[date_from]` | optional | `date` | Start date and time for filtering breakdown entries. Only entries recorded on or after this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[date_to]` | optional | `date` | End date and time for filtering breakdown entries. Only entries recorded on or before this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[subuser_id]` | optional | string | Filter by sub-user ID | 62462fcf-0938-11ec-8c4d-a4bf016cd6b2 |
| `page` | optional | integer | The current page | 1 |
| `limit` | optional | integer | The number of items to display per page | 15 |

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

