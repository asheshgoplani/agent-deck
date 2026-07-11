---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Delivery Customers
spec_version: 1.26.0622.1
---

# Delivery Customers

_5 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [GET `/delivery/customers`](#get-delivery-customers) — Get All Delivery Customers' Details
- [POST `/delivery/customers`](#post-delivery-customers) — Create a Delivery Customer
- [GET `/delivery/customers/{customer_id}`](#get-delivery-customers-customer-id) — Retrieve Delivery Customer Details
- [PUT `/delivery/customers/{customer_id}`](#put-delivery-customers-customer-id) — Update a Delivery Customer
- [DELETE `/delivery/customers/{customer_id}`](#delete-delivery-customers-customer-id) — Delete a Delivery Customer

## GET `/delivery/customers`

**Get All Delivery Customers' Details**

Retrieves a list of all delivery customers with optional filtering.

### Query Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `filter[customer_id]` | optional | string | Optional filter to retrieve customers by their unique ID. | f3a42187-0c6e-11ec-aa41-a4bf016cd6b2 |
| `filter[country_id]` | optional | `country-id` | Optional filter to retrieve customers based on their country ID. | 1 |
| `filter[date_from]` | optional | `date` | Start date and time for filtering breakdown entries. Only entries recorded on or after this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `filter[date_to]` | optional | `date` | End date and time for filtering breakdown entries. Only entries recorded on or before this date will be included. Format: YYYY-MM-DD HH:MM:SS. |  |
| `page` | optional | `page` | The current page |  |
| `limit` | optional | `limit` | The number of items to display per page |  |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`

**Content-Type:** `application/xml`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | array of `customer-base` |  |  |  |
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

## POST `/delivery/customers`

**Create a Delivery Customer**

Creates a new delivery customer.

### Request Body

JSON payload containing the data required for creating a new customer. The location information, except for country, is not required (for instance, address_line_1, address_line_2, postal_code, latitude, longitude). However, if the customer's address_line_2 is provided, address_line_1 must be included. Similarly, if the customer's latitude is provided, the longitude must also be provided, and vice versa.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `customer_id` | string |  | Unique identifier (UUID) of the customer | "f3a42187-0c6e-11ec-aa41-a4bf016cd6b2" |
| `customer_name` | `customer-name` |  |  |  |
| `email` | `email` |  | The customer's email |  |
| `contact_code` | string | null |  | The contact country code | "63" |
| `contact_number` | string | null |  | The mobile device number | "92729821" |
| `address_line_1` | string | null |  | The address line 1 | "Mcnair Road" |
| `address_line_2` | string | null |  | The address line 2 | "Boonkeng Road" |
| `postal_code` | string | null |  | The postal code | "320119" |
| `country_id` | `country-id` |  | The country ID |  |
| `latitude` | number | null |  | The latitude | 1.31972 |
| `longitude` | number | null |  | The longitude | 103.85687 |
| `subuser_id` | string | null |  | The subuser_id | "f3a42187-0c6e-11ec-aa41-a4bf016cd6b2" |
| `client_reference` | string | null |  | The client reference. | "#AB123456" |
| `create_ts` | string |  | The creation timestamp | "2023-01-01 12:00:00" |
| `update_ts` | string | null |  | The last update timestamp | "2023-01-01 12:00:00" |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  |  |  |

**Content-Type:** `application/xml`


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

## GET `/delivery/customers/{customer_id}`

**Retrieve Delivery Customer Details**

Fetches details of a specific delivery customer.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `customer_id` | **required** | `id` | Unique identifier of the customer whose details are being retrieved. | 42 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  |  |  |

**Content-Type:** `application/xml`


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

## PUT `/delivery/customers/{customer_id}`

**Update a Delivery Customer**

Updates the details of an existing delivery customer.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `customer_id` | **required** | string | Unique identifier (UUID) of the customer to be updated. | f3a42187-0c6e-11ec-aa41-a4bf016cd6b2 |

### Request Body

JSON payload containing the customer details to be updated.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `customer_id` | string |  | Unique identifier (UUID) of the customer | "f3a42187-0c6e-11ec-aa41-a4bf016cd6b2" |
| `customer_name` | `customer-name` |  |  |  |
| `email` | `email` |  | The customer's email |  |
| `contact_code` | string | null |  | The contact country code | "63" |
| `contact_number` | string | null |  | The mobile device number | "92729821" |
| `address_line_1` | string | null |  | The address line 1 | "Mcnair Road" |
| `address_line_2` | string | null |  | The address line 2 | "Boonkeng Road" |
| `postal_code` | string | null |  | The postal code | "320119" |
| `country_id` | `country-id` |  | The country ID |  |
| `latitude` | number | null |  | The latitude | 1.31972 |
| `longitude` | number | null |  | The longitude | 103.85687 |
| `subuser_id` | string | null |  | The subuser_id | "f3a42187-0c6e-11ec-aa41-a4bf016cd6b2" |
| `client_reference` | string | null |  | The client reference. | "#AB123456" |
| `create_ts` | string |  | The creation timestamp | "2023-01-01 12:00:00" |
| `update_ts` | string | null |  | The last update timestamp | "2023-01-01 12:00:00" |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  |  |  |

**Content-Type:** `application/xml`


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

## DELETE `/delivery/customers/{customer_id}`

**Delete a Delivery Customer**

Deletes a specific delivery customer identified by their customer ID.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `customer_id` | **required** | string | Unique identifier (UUID) of the customer to be deleted. | f3a42187-0c6e-11ec-aa41-a4bf016cd6b2 |

### Request Body

_No request body._

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  |  | {} |
| `meta` | object |  |  |  |

**Content-Type:** `application/xml`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  |  | {} |
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

