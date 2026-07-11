---
source: https://developer.cartrack.com/openapi/openapi.yaml
tag: Leads
spec_version: 1.26.0622.1
---

# Leads

_4 operation(s). Generated from the [Cartrack Fleet API OpenAPI spec](https://developer.cartrack.com/openapi/openapi.yaml) v1.26.0622.1._

- [POST `/leads`](#post-leads) — Create a lead policy
- [POST `/leads/potential`](#post-leads-potential) — Create a potential lead
- [POST `/leads/{policy_id}`](#post-leads-policy-id) — Upload lead policy attachment(s)
- [POST `/leads/potential/{potential_id}`](#post-leads-potential-potential-id) — Upload potential lead attachment(s)

## POST `/leads`

**Create a lead policy**

This endpoint creates a lead's policy for the given user and vehicles. Please make sure you have user's consent before posting the information.  
  
 Please note that any input will undergo the following transformations: special characters will be removed, spaces will be removed, and letters will be capitalized.

### Request Body

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `original_id` | integer | **required** | Required. The original id of the document supporting the lead policy. It must be unique | 1000123 |
| `title_type_id` | integer | **required** | The title\_type\_id must be one of the following:   | title\_type\_id | description | | --- | --- | | 1 | Mr | | 2 | Mrs | | 3 | Me | | 4 | Dr | | 5 | Prof | | 6 | Sir | | 7 | Miss | | 8 | Ms | | 9 | Minister | | 10 | Rev. | | 11 | Mr & Mrs | | 12 | Adv | | 1 |
| `first_name` | string | **required** | The first name of the lead | "John" |
| `surname` | string | **required** | The last name of the lead | "Smith" |
| `id_number` | string | **required** | The ID number of the lead. **Required only if passport is not given.** | "ABC123456" |
| `passport` | string | **required** | The passport number of the lead. **Required only if id\_number is not given.** | "YZ-101010" |
| `cell_number` | string | **required** | The cell number | "27123456789" |
| `home_number` | string |  | The home phone number | "27123456789" |
| `work_number` | string |  | The work phone number | "27123456789" |
| `email` | string | **required** | The email address | "john.smith@example.com" |
| `cartrack_username` | string | **required** | If the lead has a valid Cartrack account, please provide the username in this field. **Required only if "address" or "bank" objects are not given.** | "ABC00001" |
| `address` | object | **required** | This object must contain the lead's address details. **Required only if cartrack\_username is not given.** |  |
| `bank` | object | **required** | This object must contain the lead's bank details. **Required only if cartrack\_username is not given.** |  |
| `vehicles` | array of object | **required** | This array must contain the list of vehicle objects associated to the lead's policy. It cannot be empty |  |

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

## POST `/leads/potential`

**Create a potential lead**

This endpoint creates a potential lead. Please make sure you have user's consent before posting the information.

### Request Body

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `original_id` | string |  | The original id of the document supporting the lead policy. It must be unique | "1000123" |
| `first_name` | string |  | The first name of the lead | "John" |
| `surname` | string |  | The last name of the lead | "Smith" |
| `id_number` | string |  | The ID number of the lead. | "ABC123456" |
| `passport` | string |  | The passport number of the lead. | "YZ-101010" |
| `phone_number` | string | **required** | The phone number. **Required only if email and cell number are not given.** | "27123456789" |
| `cell_number` | string | **required** | The cell number. **Required only if email and phone number are not given.** | "27123456789" |
| `email` | string | **required** | The email address. **Required only if phone number and cell number are not given.** | "john.smith@example.com" |
| `cartrack_username` | string | **required** | Required. The Lead has a valid Cartrack account, please provide the username in this field. | "ABC00001" |
| `registration` | `registration` |  |  |  |
| `manufacturer_make_code` | string |  | If manufacturer_make_code is provided, it is not required to pass the vehicle_make and vehicle_model. | "0012" |
| `vehicle_model` | string |  | The vehicle model | "Model S" |
| `vin_number` | string |  | The Vehicle Identification Number | "JH4KA4630JC008595" |
| `engine_number` | string |  | The engine number of the vehicle | "52WVXVC10338" |
| `company_registration_number` | string |  | The company registration number | "ABC4571" |
| `company_name` | string |  | The company name | "Tesla" |
| `lead_product_id` | integer | null |  | The lead product ID. | 12215 |
| `lead_product_name` | string | null |  | The lead product name. | "Product X" |

### Responses

#### `200` — Successful operation

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object |  |  |  |

#### `400` — Validation failed for the input parameters.

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `error` | object |  | The detail of the error |  |

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

## POST `/leads/{policy_id}`

**Upload lead policy attachment(s)**

This endpoint allows you to upload file(s) to support the creation of a policy_id.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `policy_id` | **required** | integer | The policy_id returned from the leads endpoint after the POST request. | 1234567 |

### Request Body

**Content-Type:** `application/x-www-form-urlencoded`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `file` | array of string | **required** | File to be uploaded.    Only CSV, XLSX, XLS, and PDF formats are accepted.     Note: Maximum accepted file size: 10 MB. |  |

### Responses

#### `200` — Successful uploaded

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

## POST `/leads/potential/{potential_id}`

**Upload potential lead attachment(s)**

This endpoint allows you to upload file(s) to support the creation of a potential_id.

### Path Parameters

| Name | Required | Type | Description | Example |
|---|---|---|---|---|
| `potential_id` | **required** | integer | The potential_id returned from the leads/potential endpoint after the POST request. | 1234567 |

### Request Body

**Content-Type:** `application/x-www-form-urlencoded`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `file` | array of string | **required** | File to be uploaded.    Only CSV, XLSX, XLS, and PDF formats are accepted.     Note: Maximum accepted file size: 10 MB. |  |

### Responses

#### `200` — Successful uploaded

**Content-Type:** `application/json`


| Field | Type | Required | Description | Example |
|---|---|---|---|---|
| `data` | object | null |  |  |  |
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

