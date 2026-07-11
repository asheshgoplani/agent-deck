---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Delivery Drivers
spec_version: 1.26.0622.1
---

# Delivery Drivers

_6 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/delivery/drivers`](#get-delivery-drivers) — Retrieve All Delivery Drivers' Details
- [POST `/delivery/drivers`](#post-delivery-drivers) — Create Delivery Drivers
- [GET `/delivery/drivers/{driver_id}`](#get-delivery-drivers-driver-id) — Retrieve Delivery Driver Details
- [PUT `/delivery/drivers/{driver_id}`](#put-delivery-drivers-driver-id) — Update Delivery Driver
- [DELETE `/delivery/drivers/{driver_id}`](#delete-delivery-drivers-driver-id) — Deactivate Delivery Driver
- [GET `/delivery/drivers/{driver_id}/jobs`](#get-delivery-drivers-driver-id-jobs) — Retrieve All Jobs for a Delivery Driver

## GET `/delivery/drivers`

**Retrieve All Delivery Drivers' Details**

Fetches details of all delivery drivers with optional filtering.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[delivery_driver_id]` | optional | string | Optional filter to retrieve drivers by their unique ID. | f3a42187-0c6e-11ec-aa41-a4bf016cd6b2 |
| `filter[driver_status_id]` | optional | `driver-status-id` | Optional filter to retrieve drivers based on their status (1 = Online, 2 = On Route, 3 = Not Active, 4 = Offline, 5 = On Break). | 2 |
| `filter[date_from]` | optional | `date` | Start date and time for filtering breakdown entries. Only entries recorded on or after this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[date_to]` | optional | `date` | End date and time for filtering breakdown entries. Only entries recorded on or before this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `delivery-driver-response` |  |  |  |
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

#### `500` — Internal server error.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |


---

## POST `/delivery/drivers`

**Create Delivery Drivers**

Creates a new delivery driver with specified details.

### Request Body

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `first_name` | string | **required** | Driver first name | "Steven" |
| `last_name` | string |  | Driver last name | "Mark" |
| `phone_code` | `phone-code` | **required** |  |  |
| `phone_number` | integer | **required** | Mobile device number | 9449912 |
| `email` | `email` |  |  |  |
| `gender` | integer |  | Gender 1 for male, 2 for female | 1 |
| `login_username` | string | null |  | The unique username. |  |
| `password` | string | null |  | The password to verify access. |  |
| `registration` | `registration` |  |  |  |
| `is_active` | boolean |  | Activate/Deactivates account | true |
| `subuser_id` | string | null |  | Subuser ID | "62462fcf-0938-11ec-8c4d-a4bf016cd6b2" |
| `shift_time_start` | string | null |  | The start time of the driver's shift in HH:MM:SS+TZ format (e.g. 06:30:00+08:00) | "12:00:00+08:00" |
| `shift_time_end` | string | null |  | The end time of the driver's shift in HH:MM:SS+TZ format (e.g. 22:30:00+08:00) | "12:00:00+08:00" |
| `start_location_customer_id` | string | null |  | Start location customer ID | "f3a42187-0c6e-11ec-aa41-a4bf016cd6b2" |
| `end_location_customer_id` | string | null |  | End location customer ID | "f3a42187-0c6e-11ec-aa41-a4bf016cd6b2" |
| `max_weight` | number |  | Maximum weight the driver can carry | 1000 |
| `max_volume` | number |  | Maximum volume the driver can carry | 1000 |
| `special_equipment` | array of integer |  | List of special equipment the driver can handle |  |
| `pin_code` | integer |  | Driver's PIN code. Must be between 4 and 8 digits (e.g. 1234, 123456, 12345678).     If this field is not provided, the PIN code will not be created. | 123456 |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`

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

## GET `/delivery/drivers/{driver_id}`

**Retrieve Delivery Driver Details**

Fetches the details of a specific delivery driver.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `driver_id` | **required** | string |  | f3a42187-0c6e-11ec-aa41-a4bf016cd6b2 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`

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

#### `500` — Internal server error.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |


---

## PUT `/delivery/drivers/{driver_id}`

**Update Delivery Driver**

Update an existing delivery driver

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `driver_id` | **required** | string | Unique UUID identifying the driver whose details are to be updated | f3a42187-0c6e-11ec-aa41-a4bf016cd6b2 |

### Request Body

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `first_name` | string | null |  | The driver's first name     If null is provided, the value will be ignored during update. This field does not support null update. | "Steven" |
| `last_name` | string | null |  | The driver's last name | "Mark" |
| `phone_code` | `phone-code` |  | The country contact code. This field is required when phone number is provided.     If null is provided, the value will be ignored during update. This field does not support null update. |  |
| `phone_number` | integer |  | The mobile device number. This field is required when phone code is provided.     If null is provided, the value will be ignored during update. This field does not support null update. | 9449912 |
| `email` | `email` |  | The email address of the driver     If null is provided, the value will be ignored during update. This field does not support null update. |  |
| `gender` | integer |  | Gender 1 for male, 2 for female | 1 |
| `login_username` | string | null |  | The unique username     If null is provided, the value will be ignored during update. This field does not support null update. |  |
| `password` | string | null |  | The password to verify access     When empty, password will not be updated |  |
| `registration` | `registration` |  |  |  |
| `is_active` | boolean |  | Activate/Deactivates account | true |
| `subuser_id` | string | null |  | Subuser ID | "62462fcf-0938-11ec-8c4d-a4bf016cd6b2" |
| `shift_time_start` | string | null |  | Start time of driver's shift in HH:MM:SS+TZ format | "12:00:00+08:00" |
| `shift_time_end` | string | null |  | End time of driver's shift in HH:MM:SS+TZ format | "12:00:00+08:00" |
| `start_location_customer_id` | string | null |  | Start location customer ID | "f3a42187-0c6e-11ec-aa41-a4bf016cd6b2" |
| `end_location_customer_id` | string | null |  | End location customer ID | "f3a42187-0c6e-11ec-aa41-a4bf016cd6b2" |
| `max_weight` | number |  | Maximum weight the driver can carry | 1000 |
| `max_volume` | number |  | Maximum volume the driver can carry | 1000 |
| `special_equipment` | array of integer |  | List of special equipment the driver can handle |  |
| `pin_code` | integer |  | Driver's PIN code. Must be between 4 and 8 digits (e.g. 1234, 123456, 12345678).     If this field is not provided, the PIN code will not be updated. | 123456 |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`

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

## DELETE `/delivery/drivers/{driver_id}`

**Deactivate Delivery Driver**

Deactivate a delivery driver by ID

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `driver_id` | **required** | string | Unique identifier of the driver to be deactivated. | f3a42187-0c6e-11ec-aa41-a4bf016cd6b2 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

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

#### `404` — The requested resource was not found.

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

## GET `/delivery/drivers/{driver_id}/jobs`

**Retrieve All Jobs for a Delivery Driver**

Fetches a list of all jobs assigned to a specific delivery driver, with optional filtering.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `driver_id` | **required** | string |  | f3a42187-0c6e-11ec-aa41-a4bf016cd6b2 |

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[job_id]` | optional | `job-id` | Optional filter to retrieve jobs by their unique ID. |  |
| `filter[order_id]` | optional | string | Optional filter to retrieve jobs by associated order ID. | 20210908000021 |
| `filter[schedule_type_id]` | optional | integer | Optional filter to retrieve jobs based on schedule type (1 = ASAP, 2 = Scheduled, 3 = Unscheduled). | 2 |
| `filter[job_status_id]` | optional | integer | Optional filter to retrieve jobs by their status (2 = Assign Later, 3 = Rejected/Failed, 4 = Assigned, 5 = Completed). | 3 |
| `filter[create_ts_from]` | optional | `date` | Optional filter to retrieve jobs created after a specified date and time, based on the create_ts attribute of the parent job object. | 2023-01-01 12:00:00 |
| `filter[create_ts_to]` | optional | `date` | Optional filter to retrieve jobs created before a specified date and time, based on the create_ts attribute of the parent job object. | 2023-01-01 12:00:00 |
| `filter[delivery_ts_from]` | optional | `date` | Optional filter to retrieve jobs with a delivery time starting from a specified date and time, based on the scheduled_delivery_ts attribute of the parent job object. | 2023-01-01 12:00:00 |
| `filter[delivery_ts_to]` | optional | `date` | Optional filter to retrieve jobs with a delivery time up until a specified date and time, based on the scheduled_delivery_ts attribute of the parent job object. | 2023-01-01 12:00:00 |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |
| `sort` | optional | string | Determines the sorting order of the job data; use "+" for ascending and "-" for descending order. | +create_ts,-registration,driver_status_id |

### Request Body

_No request body._

### Responses

#### `200` — List of driver jobs retrieved successfully

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

#### `500` — Internal server error.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |


---

