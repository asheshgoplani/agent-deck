---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Geofences
spec_version: 1.26.0622.1
---

# Geofences

_9 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/geofences`](#get-geofences) — Retrieve All Geofences
- [POST `/geofences`](#post-geofences) — Create a Geofence
- [GET `/geofences/{geofence_id}`](#get-geofences-geofence-id) — Retrieve a Geofence
- [PUT `/geofences/{geofence_id}`](#put-geofences-geofence-id) — Update a Geofence
- [DELETE `/geofences/{geofence_id}`](#delete-geofences-geofence-id) — Delete a Geofence
- [GET `/geofences/visitors`](#get-geofences-visitors) — Retrieve All Geofence Visitors
- [GET `/geofences/visits`](#get-geofences-visits) — Retrieve All Geofence Visits
- [POST `/geofences/vehicle/createAlert`](#post-geofences-vehicle-createalert) — Create a Vehicle Geofence Alert
- [GET `/geofences/{geofence_id}/visitors`](#get-geofences-geofence-id-visitors) — Retrieve Geofence Visitors by ID

## GET `/geofences`

**Retrieve All Geofences**

This endpoint retrieves a list of all geofences or a filtered subset, based on the provided query parameters.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[name]` | optional | string | Filter the geofences by name. Supports case-insensitive partial matching. | "office" |
| `filter[description]` | optional | string | Filter the geofences by description. Supports case-insensitive partial matching. | "home" |
| `filter[subuser_id]` | optional | string | Filter by sub-user ID | 62462fcf-0938-11ec-8c4d-a4bf016cd6b2 |
| `filter[position_description]` | optional | string | Filter the geofences by position description. Supports case-insensitive partial matching. | "Example street" |
| `filter[colour]` | optional | string | Filter the geofences by colour. Supports case-insensitive partial matching. | "#ff52" |
| `filter[hide_subusers]` | optional | boolean | Toggle between displaying only main user geofences or all. Set to true to hide subuser geofences. | true |
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

## POST `/geofences`

**Create a Geofence**

This endpoint is used for creating a new geofence by specifying its parameters.

### Request Body

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `polygon` | array of object | **required** | If you do not need a complex geofence, you should use the circle property instead. Note that the POLYGON's first and last coordinates must be the same to form a closed shape. **This property is only required if a circle property is not given.** |  |
| `circle` | object | **required** | If you need a simpler circular geofence instead of a more complex polygon shape, use this property. The circle will only require a radius (not a diameter), a latitude, and a longitude to create the geofence. **This property is only required if a polygon property is not given.** |  |
| `name` | string | **required** | The name of the geofence | "Parking" |
| `description` | string | null |  | The description of the geofence | "Supermarket parking lot" |
| `colour` | string |  | The color of the geofence | "#ce5239" |
| `subuser_id` | string |  | The subuser id to associate with this geofence | "62462fcf-0938-11ec-8c4d-a4bf016cd6b2" |
| `geofence_group_ids` | array of integer |  | Add the geofence to specific geofence groups. |  |
| `vehicle_ids` | array of integer |  | The vehicle IDs associated to the geofence. |  |

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

## GET `/geofences/{geofence_id}`

**Retrieve a Geofence**

This endpoint retrieves the details of a specific geofence using its unique ID.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `geofence_id` | **required** | `geofence-id` | The geofence's ID | 7489ff44-xxxx-11eb-xxxx-005056a801ca |

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

## PUT `/geofences/{geofence_id}`

**Update a Geofence**

This endpoint updates specific parameters of an existing geofence, identified by its unique ID.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `geofence_id` | **required** | `geofence-id` | The unique identifier (UUID) of the geofence that is being updated. | 7489ff44-xxxx-11eb-xxxx-005056a801ca |

### Request Body

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `polygon` | array of object |  | If you do not need a complex geofence, you should use the circle property instead. Note that the POLYGON's first and last coordinates must be the same to form a closed shape. **This property is only required if a circle property is not given.** |  |
| `circle` | object |  | If you need a simpler circular geofence instead of a more complex polygon shape, use this property. The circle will only require a radius (not a diameter), a latitude, and a longitude to create the geofence. |  |
| `name` | string |  | The name of the geofence | "Parking" |
| `description` | string | null |  | The description of the geofence | "Supermarket parking lot" |
| `colour` | string |  | The color of the geofence | "#ce5239" |
| `subuser_id` | string | null |  | The subuser id to associate with this geofence | "62462fcf-0938-11ec-8c4d-a4bf016cd6b2" |
| `geofence_group_ids` | array of integer |  | Add the geofence to specific geofence groups. |  |
| `vehicle_ids` | array of integer |  | The vehicle IDs associated to the geofence. |  |

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

## DELETE `/geofences/{geofence_id}`

**Delete a Geofence**

This endpoint deletes an existing geofence identified by its unique ID.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `geofence_id` | **required** | `geofence-id` | The unique identifier (UUID) of the geofence to be deleted. | 7489ff44-xxxx-11eb-xxxx-005056a801ca |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  | An empty object | {} |
| `meta` | object |  | This metadata will contain a message |  |

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

## GET `/geofences/visitors`

**Retrieve All Geofence Visitors**

This endpoint retrieves a list of all visitors for all geofences, with support for pagination.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
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

## GET `/geofences/visits`

**Retrieve All Geofence Visits**

This endpoint retrieves a list of all visits for all geofences, 
with support for pagination.

Geofence visit records are generated only after a vehicle's trip has ended
(on IGN_OFF) and are not created in real time.
Queries made while a vehicle is still inside a geofence will not return a
visit record for the active visit.


### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[vehicle_id]` | optional | integer | Optional filter to retrieve by vehicle ID. | 12345 |
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[geofence_id]` | optional | string | Filter groups that contains a geofence ID, an exact match | "62462fcf-0938-11ec-8c4d-a4bf016cd6b2" |
| `filter[geofence_name]` | optional | string | Filter based on geofence name. | "Sample geofence name" |
| `filter[enter_timestamp]` | **required** | `date` | Filter based on enter timestamp.       The lookup period is limited to a maximum of **24 hours** between enter\_timestamp and exit\_timestamp or the past 24 hours from the current time if exit\_timestamp is not provided. | "2025-03-08 18:22:23" |
| `filter[exit_timestamp]` | optional | `date` | Filter based on exit timestamp.       The lookup period is limited to a maximum of **24 hours** between enter\_timestamp and exit\_timestamp. | "2025-03-08 19:22:23" |
| `sort_by` | optional | string | Sort the geofence visits based on the specified field. Default is `enter_timestamp`. | "enter_timestamp" |
| `sort_order` | optional | string | Sort order for the geofence visits. Default is `asc`. | "asc" |
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

## POST `/geofences/vehicle/createAlert`

**Create a Vehicle Geofence Alert**

This endpoint is designed for creating a geofence alert associated with a specific vehicle. It enables the monitoring of the vehicle's entry and exit at the defined geofence. Note that setting up a new geofence alert for a vehicle will automatically remove any previous geofence alerts for that vehicle. As such, only one geofence alert can be active for a vehicle at any given time. The entry and exit will be reported inside the GET /notifications API.

### Request Body

A set of parameters necessary for setting up a vehicle geofence alert. This includes geofence details like the geographical coordinates, boundary radius, and the specific vehicle identifier to associate with the alert.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `registration` | `registration` | **required** | The license plate of the vehicle |  |
| `latitude` | number | **required** | The latitude coordinate of the geofence's center point, expressed as a decimal. | 1.37338 |
| `longitude` | number | **required** | The longitude coordinate of the geofence's center point, expressed as a decimal. | 103.68629 |
| `radius` | integer |  | Defines the radius of the geofence in meters, creating a circular boundary around the center point. |  |

### Responses

#### `200` — Alert created successfully

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | null |  |  |  |
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

## GET `/geofences/{geofence_id}/visitors`

**Retrieve Geofence Visitors by ID**

This endpoint retrieves all visitors for a specified geofence, identified by the geofence ID.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `geofence_id` | **required** | `geofence-id` | The unique identifier (UUID) of the geofence for which visitors are being retrieved. | 7489ff44-xxxx-11eb-xxxx-005056a801ca |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  |  |  |

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

