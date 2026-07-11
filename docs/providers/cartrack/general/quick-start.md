---
source: https://developer.cartrack.com/docs/fleet-api-general/quick-start
title: Quick Start
---

# Quick Start

# Quick Start

tip

Get started immediately and perform your first API request to retrieve the vehicles list [here](</docs/fleet-api/get-vehicles-list>).

Getting started with the Cartrack Fleet API is straightforward. This section provides a basic example of how to make your first API call using curl, JavaScript, or Python.

## Prerequisites​

Before you proceed, make sure you have:

  * **An HTTP client** : curl (available on Unix, Linux, macOS, and Windows), or an HTTP library in your language of choice (e.g. `requests` for Python, `fetch` for JavaScript).
  * **Base URL and Authentication Credentials** : You will need the base URL for the Cartrack Fleet API and valid credentials. For detailed guidance on obtaining these, please refer to the [Authentication](</docs/fleet-api-general/authentication>) and [Base URLs](</docs/fleet-api-general/base-url>) sections of our documentation.

## Making an API Call​

The example below retrieves the list of vehicles. Replace `{baseUrl}` with your regional base URL and supply your credentials (see [Authentication](</docs/fleet-api-general/authentication>)).

  * curl
  * JavaScript
  * Python



    curl --location '{baseUrl}/rest/vehicles' \


      --header 'Accept: application/json' \


      --header 'Authorization: Basic your_encoded_credentials'



    const credentials = btoa('username:password');





    const response = await fetch('{baseUrl}/rest/vehicles', {


      headers: {


        'Accept': 'application/json',


        'Authorization': `Basic ${credentials}`,


      },


    });





    const data = await response.json();


    console.log(data);



    import requests





    response = requests.get(


        '{base_url}/rest/vehicles',


        auth=('username', 'password'),


        headers={'Accept': 'application/json'},


    )





    print(response.json())


## Response​

The API will return a JSON response containing a list of vehicles, each with details like vehicle ID, model, and other relevant information.

For more detail on the available endpoints and request/response schemas, see the [Fleet API reference](</docs/fleet-api/fleet-api>).

## Import to Postman​

You can import the Cartrack OpenAPI specification into Postman to generate a ready-to-use collection of requests.

  * Spec URL: <https://developer.cartrack.com/openapi/openapi.yaml>
  * In Postman: click **Import → Link** , paste the URL, then click **Import**. Or use **Import → File** and upload the downloaded `openapi.yaml`.
  * Postman will create a collection of requests based on the spec.
  * Configure environment values:
    * Set the base URL using the guidance on the [Base URLs](</docs/fleet-api-general/base-url>) page.
    * Configure authentication: Cartrack APIs use **Basic Authentication**. In Postman, open any request, go to the **Authorization** tab, choose **Basic Auth** , and enter your username and password. Alternatively, add the `Authorization` header with the value `Basic <base64-encoded-credentials>`. See the [Authentication](</docs/fleet-api-general/authentication>) page for details on generating credentials.
  * Run individual requests or use the Collection Runner to execute multiple calls.
