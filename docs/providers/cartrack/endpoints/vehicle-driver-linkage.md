---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Vehicle Driver Linkage
spec_version: 1.26.0622.1
---

# Vehicle Driver Linkage

_4 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [POST `/vehicles/drivers/historical`](#post-vehicles-drivers-historical) — Patch Historical Vehicle Data
- [POST `/vehicles/drivers/link`](#post-vehicles-drivers-link) — Link Driver to a Vehicle
- [DELETE `/vehicles/drivers/link`](#delete-vehicles-drivers-link) — Unlink Driver from a Vehicle
- [GET `/vehicles/drivers/links`](#get-vehicles-drivers-links) — Retrieve Current Linkages

## POST `/vehicles/drivers/historical`

**Patch Historical Vehicle Data**

This endpoint updates historical vehicle data by patching driver information for periods where a vehicle-driver linkage was not updated promptly with the correct driver.
  
  
**Note**: Vehicle Driver Linkage endpoints must **NOT** be used alongside other Cartrack driver-vehicle assignment services (e.g., driver mobile app, driver tags).
  
  
**Warning**: This patching action is irreversible. Exercise caution and ensure all considerations are addressed before proceeding with any modifications.

### Request Body

A JSON payload containing the data required to patch historical vehicle data.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `registration` | `registration` | **required** |  |  |
| `start_timestamp` | `date` | **required** | Updates historical vehicle data from the specified date and time.       The lookup period is limited to a maximum of **24 hours** between start\_timestamp and end\_timestamp or the past 24 hours from the current time if end\_timestamp is not provided. | "2025-01-01 00:00:00" |
| `end_timestamp` | `date` |  | Updates historical vehicle data up to the specified date and time.       The lookup period is limited to a maximum of **24 hours** between start\_timestamp and end\_timestamp. | "2025-01-02 00:00:00" |
| `employee_number` | string |  | The driver's employee number. Provide either this, driver\_name, or driver\_id, but not all.        Must be unique within your driver pool; Request will fail if duplicates exist. | "11221001" |
| `driver_name` | string |  | The driver's name. Provide either this, employee\_number, or driver\_id, but not all.       Case-insensitive exact match on "driver\_name driver\_surname". Must be unique within your driver pool; Request will fail if duplicates exist. | "Mark Steven" |
| `driver_id` | string | null |  | The driver's unique identifier. Provide either this, employee_number, or driver_name, but not all. | "62462fcf-0938-11ec-8c4d-a4bf016cd6b2" |

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

## POST `/vehicles/drivers/link`

**Link Driver to a Vehicle**

This endpoint links a driver to a vehicle, ensuring subsequent vehicle data includes the driver info. Any previous API-created linkage for the same vehicle or driver will be overwritten.
  
  
**Note**: Vehicle Driver Linkage endpoints must **NOT** be used alongside other Cartrack driver-vehicle assignment services (e.g., driver mobile app, driver tags).

### Request Body

A JSON payload containing the data required to link a driver to a vehicle.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `registration` | `registration` | **required** |  |  |
| `employee_number` | string |  | The driver's employee number. Provide either this, driver\_name, or driver\_id, but not all.       Must be unique within your driver pool; Request will fail if duplicates exist. | "11221001" |
| `driver_name` | string |  | The driver's name. Provide either this, employee\_number, or driver\_id, but not all.       Case-insensitive exact match on "driver\_name driver\_surname". Must be unique within your driver pool; Request will fail if duplicates exist. | "Mark Steven" |
| `driver_id` | string | null |  | The driver's unique identifier. Provide either this, employee_number, or driver_name, but not all. | "62462fcf-0938-11ec-8c4d-a4bf016cd6b2" |

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

## DELETE `/vehicles/drivers/link`

**Unlink Driver from a Vehicle**

This endpoint unlinks a driver from a vehicle, removing the association so that subsequent vehicle data will no longer include the driver info. Any existing linkage created via API for the requested vehicle and/or driver will be removed.  
  
 **Note**: Vehicle Driver Linkage endpoints must **NOT** be used alongside other Cartrack driver-vehicle assignment services (e.g., driver mobile app, driver tags).

### Request Body

A JSON payload containing the data required to unlink a driver from a vehicle.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `registration` | `registration` | **required** |  |  |
| `force` | boolean |  | The force flag enables deletion of the vehicle-driver linkage without requiring driver details (employee_number, driver_name, or driver_id). | true |
| `employee_number` | string |  | The driver's employee number. Provide either this, driver\_name, or driver\_id, but not all.       Must be unique within your driver pool; Request will fail if duplicates exist. | "11221001" |
| `driver_name` | string |  | The driver's name. Provide either this, employee\_number, or driver\_id, but not all.       Case-insensitive exact match on "driver\_name driver\_surname". Must be unique within your driver pool; Request will fail if duplicates exist. | "Mark Steven" |
| `driver_id` | string |  | The driver's unique identifier. Provide either this, employee_number, or driver_name, but not all. | "62462fcf-0938-11ec-8c4d-a4bf016cd6b2" |

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

## GET `/vehicles/drivers/links`

**Retrieve Current Linkages**

This endpoint retrieves a list of linkages, providing details of currently linked drivers and vehicles. The retrieved data reflects only linkages created via the API.
  
  
**Note**: Vehicle Driver Linkage endpoints must **NOT** be used alongside other Cartrack driver-vehicle assignment services (e.g., driver mobile app, driver tags).

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[registration]` | optional | `registration` | Filter by vehicle registration. |  |
| `filter[driver_id]` | optional | string | Filter by driver id (exact match). | 02870802-xxxx-42ed-xxxx-c999df353f42 |
| `filter[driver_name]` | optional | string | The driver's name.      Case-insensitive exact match on "driver\_name driver\_surname". Must be unique in your driver pool. | "Mark Steven" |
| `filter[employee_number]` | optional | string | The driver's employee number. | "1101123" |
| `page` | optional | integer | The current page | 1 |
| `limit` | optional | integer | The number of items to display per page | 15 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of object |  | The list of events generated during that period |  |
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

