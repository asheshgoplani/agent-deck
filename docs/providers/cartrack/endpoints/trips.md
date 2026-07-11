---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Trips
spec_version: 1.26.0622.1
---

# Trips

_4 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/trips`](#get-trips) — Get All Trips
- [GET `/trips/{registration}`](#get-trips-registration) — Get Trips By Registration
- [PUT `/trips/{registration}`](#put-trips-registration) — Update Trip Information
- [GET `/trips/elapsed`](#get-trips-elapsed) — Get All Trips Elapsed

## GET `/trips`

**Get All Trips**

Returns a paginated list of all trips associated with your account that overlap
the specified time range.

A trip is included if it was active at any point within the requested window —
this means a trip that **started before** `start_timestamp` but ended within
the range will be included, and a trip that **started within** the range but
ended after `end_timestamp` will also be included. As a result, returned trips
may have `start_timestamp` or `end_timestamp` values that fall **outside** the
requested range and may span across different calendar dates.

The lookup window is limited to a **maximum of 31 days** between
`start_timestamp` and `end_timestamp`.

Data is available for approximately the last 5 years. Requests with
`start_timestamp` earlier than 5 years ago will be rejected with a 422 error.

> **Note:** If you are computing total distance travelled for a specific day
> by summing `trip_distance`, the result may be inaccurate. Trips that started
> before `start_timestamp` or ended after `end_timestamp` are included in full,
> so their distance will be over- or under-counted relative to the requested
> window. For accurate daily distance figures, use
> `GET /vehicles/{registration}/odometer` instead.


### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `start_timestamp` | **required** | `date` | Start date and time to retrieve events from. | 2022-01-01 00:00:00 |
| `end_timestamp` | **required** | `date` | End date and time to retrieve events up to. The lookup period has a **maximum of 31 days**. | 2022-01-01 23:59:59 |
| `incl_private` | optional | boolean | Setting this to true will include private trips data in the results    Private trips are controlled and only a limited set of data will be returned | false |
| `page` | optional | integer | The current page | 1 |
| `limit` | optional | integer | The number of items to display per page | 15 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  | This array returns the list of trips |  |
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

## GET `/trips/{registration}`

**Get Trips By Registration**

Returns a paginated list of trips for a specific vehicle, identified by its
license plate, that overlap the specified time range.

A trip is included if it was active at any point within the requested window —
this means a trip that **started before** `start_timestamp` but ended within
the range will be included, and a trip that **started within** the range but
ended after `end_timestamp` will also be included. As a result, returned trips
may have `start_timestamp` or `end_timestamp` values that fall **outside** the
requested range and may span across different calendar dates.

The lookup window is limited to a **maximum of 31 days** between
`start_timestamp` and `end_timestamp`.

Data is available for approximately the last 5 years. Requests with
`start_timestamp` earlier than 5 years ago will be rejected with a 422 error.

> **Note:** If you are computing total distance travelled for a specific day
> by summing `trip_distance`, the result may be inaccurate. Trips that started
> before `start_timestamp` or ended after `end_timestamp` are included in full,
> so their distance will be over- or under-counted relative to the requested
> window. For accurate daily distance figures, use
> `GET /vehicles/{registration}/odometer` instead.


### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `registration` | **required** | `registration` | The license plate of the vehicle | ABC1234 |

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `start_timestamp` | **required** | `date` | Start date and time to retrieve events from. | 2022-01-01 00:00:00 |
| `end_timestamp` | **required** | `date` | End date and time to retrieve events up to. The lookup period has a **maximum of 31 days**. | 2022-01-01 23:59:59 |
| `incl_private` | optional | boolean | Setting this to true will include private trips data in the results    Private trips are controlled and only a limited set of data will be returned | false |
| `page` | optional | integer | The current page | 1 |
| `limit` | optional | integer | The number of items to display per page | 15 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  | This array returns the list of trips |  |
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

## PUT `/trips/{registration}`

**Update Trip Information**

This endpoint allows you to update the trip type, trip title and/or extra trip notes by specifying the vehicle registration and a timestamp. This can be useful if you want to automate the categorisation of your trips or add any additional information.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `registration` | **required** | `registration` | The license plate of the vehicle | "ABC1234" |

### Request Body

The json data that needs to be processed

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `trip_title` | string | **required** | The name or title of the trip you want to update. | "ABC Cargo Services Inc." |
| `trip_type` | string | **required** | The type of the trip you want to update, which can be one of the following values: "None," "Business," or "Private." | "Business" |
| `trip_extra_notes` | string |  | Any additional notes or details related to the trip. | "ABC Cargo Services Inc. Other Description" |
| `start_timestamp` | `date` | **required** | The date and time when the trip you want to update started. Use the format "YYYY-MM-DD HH:MM:SS". | "2023-01-13 02:53:24" |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  |  |  |
| `meta` | object |  |  |  |

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

## GET `/trips/elapsed`

**Get All Trips Elapsed**

Returns a list of all trips with elapsed time within the specified time range, along with optional filtering and sorting capabilities. Data is available for approximately the last 5 years. Requests with `start_timestamp` earlier than 5 years ago will be rejected with a 422 error.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `start_timestamp` | **required** | `date` | Start date and time to retrieve events from. | 2022-01-01 00:00:00 |
| `end_timestamp` | **required** | `date` | End date and time to retrieve events up to. The lookup period has a **maximum of 31 days**. | 2022-01-01 23:59:59 |
| `filter[vehicle_id]` | optional | integer | Optional filter to retrieve by vehicle ID. | 12345 |
| `filter[driver_id]` | optional | string | Filter by driver id (exact match). | 02870802-xxxx-42ed-xxxx-c999df353f42 |
| `sort_by` | optional | string | Sort by a specific field. Default is sorted by start_timestamp. | start_timestamp |
| `sort_order` | optional | string | Sort order for the specified field. Default is ascending order. | asc |
| `page` | optional | integer | The current page | 1 |
| `limit` | optional | integer | The number of items to display per page | 15 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  | This array returns the list of trips |  |
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

