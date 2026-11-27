# Referencing request values
There are four types of request values:

1. HTTP Request Header values

    ```json
    {
      "source": "header",
      "name": "Header-Name"
    }
    ```

2. HTTP Query parameters

    ```json
    {
      "source": "url",
      "name": "parameter-name"
    }
    ```

3. HTTP Request parameters
    #### Valid `name` Parameters:
    - `"name": "method"`
    - `"name": "remote-addr"`

    *Note* Anything other than above mentioned `name` parameters would be invalid!

    ```json
    {
      "source": "request",
      "name": "method"
    }
    ```

    ```json
    {
      "source": "request",
      "name": "remote-addr"
    }
    ```

4. Payload (JSON or form-value encoded)
    ```json
    {
      "source": "payload",
      "name": "parameter-name"
    }
    ```

    *Note:* For JSON encoded payload, you can reference nested values using the dot-notation.
    For example, if you have following JSON payload
 
    ```json
    {
      "commits": [
        {
          "commit": {
            "id": 1
          }
        }, {
          "commit": {
            "id": 2
          }
        }
      ]
    }
    ```

    You can reference the first commit id as

    ```json
    {
      "source": "payload",
      "name": "commits.0.commit.id"
    }
    ```

    If the payload contains a key with the specified name "commits.0.commit.id", then the value of that key has priority over the dot-notation referencing.

4. XML Payload

    Referencing XML payload parameters is much like the JSON examples above, but XML is more complex.
    Element attributes are prefixed by a hyphen (`-`).
    When an element has attributes or child elements, its nonempty text is available under `#text`;
    otherwise, its text is the element's value.
    Namespaced element and attribute names use `{URI}local-name`, where the URI
    and local name are URL query-escaped and dots are escaped as `%2E` so they
    can be used in dot paths. For example, an element `p:item` in namespace
    `https://example.com/a` is referenced as
    `{https%3A%2F%2Fexample%2Ecom%2Fa}item`; an attribute in that namespace
    uses the same notation after the `-` prefix. Prefixes bound to the same URI
    identify the same namespace. Unnamespaced names remain unchanged.
    Namespace declarations are represented as attributes, such as `-xmlns` for
    a default namespace or
    `-{http%3A%2F%2Fwww%2Ew3%2Eorg%2F2000%2Fxmlns%2F}p` for a prefix declaration.

    Text fragments within an element are concatenated in document order, with
    leading and trailing whitespace trimmed. The XML document must contain one
    root element; only whitespace, comments, and processing instructions may
    follow it. Declared encodings supported by the IANA encoding registry are
    converted to UTF-8.

    Take the following XML payload:

    ```xml
    <app>
      <users>
        <user id="1" name="Jeff" />
        <user id="2" name="Sally" />
      </users>
      <messages>
        <message id="1" from_user="1" to_user="2">Hello!!</message>
      </messages>
    </app>
    ```

    To access a given `user` element, you must treat them as an array.
    So `app.users.user.0.-name` yields `Jeff`.

    Since there's only one `message` tag, it's not treated as an array.
    So `app.messages.message.-id` yields `1`.

    To access the text within the `message` tag, you would use: `app.messages.message.#text`.

If you are referencing values for environment, you can use `envname` property to set the name of the environment variable like so
```json
{
  "source": "url",
  "name": "q",
  "envname": "QUERY"
}
``` 
to get the QUERY environment variable set to the `q` parameter passed in the query string.

# Special cases
If you want to pass the entire payload as JSON string to your command you can use
```json
{
  "source": "entire-payload"
}
```

for headers you can use
```json
{
  "source": "entire-headers"
}
```

and for query variables you can use
```json
{
  "source": "entire-query"
}
```
