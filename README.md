# transform-to-omnipub

A command‑line Go tool to read JSON article files, transform them into HTML + metadata, and upload to the Omnipub API.

## Features

- Scans a directory for `*.json` files conforming to the expected schema  
- Cleans and builds HTML content from article fields  
- Assembles metadata (title, subtitle, creation date, cover image, external IDs)  
- Sends each item as `multipart/form-data` to `POST /api/v2/omnipub`  
- Counts and reports successful vs. failed uploads  
- Supports high concurrency with configurable worker pool and connection limits  
- Handles rate limiting with configurable backoff intervals
- Supports retrying failed uploads from a list file

## Installation

1. **Clone the repository**  
   ```bash
   git clone https://github.com/cashmere-data/transform-to-omnipub.git
   cd transform-to-omnipub
   ```

2. **Initialize Go modules**  
   ```bash
   go mod init transform-to-omnipub
   go mod tidy
   ```

3. **Build the binary**  
   ```bash
   go build -o transform transform.go
   ```

> You can also run directly with `go run transform.go`.

## Configuration

- **API Key**: Set your Omnipub API key in the environment:
  ```bash
  export OMNIPUB_API_KEY="your_actual_api_key"
  ```
- **Environment variable name** can be customized via the `-key-env` flag (defaults to `OMNIPUB_API_KEY`).

## Usage

```bash
transform [flags]
```

### Flags

| Flag           | Default                     | Description                                    |
| -------------- | --------------------------- | ---------------------------------------------- |
| `-dir`         | `.`                         | Directory containing `*.json` files            |
| `-retry`       | `""`                        | File with list of failed files to retry        |
| `-api`         | `https://api.example.com/v2`| Base URL for the Omnipub API                   |
| `-collection`  | `0`                         | (Optional) Collection ID to attach             |
| `-workers`     | `10`                        | Number of concurrent upload workers            |
| `-backoff`     | `0`                         | Milliseconds to wait between requests (rate limiting) |
| `-max-conns`   | `256`                       | Max connections per host (configures transport)|
| `-key-env`     | `OMNIPUB_API_KEY`          | ENV var name holding the API key               |
| `-save-failures` | `""`                      | Save failed file paths to this file            |

### Examples

1. **Basic run**  
   ```bash
   export OMNIPUB_API_KEY="abc123"
   transform -dir ./json_files
   ```

2. **Custom API base & collection**  
   ```bash
   export MY_KEY="abc123"
   transform -dir data -api https://api.example.com/v2 \
             -collection 42 \
             -key-env MY_KEY
   ```

3. **High‑throughput upload**  
   ```bash
   export OMNIPUB_API_KEY="xyz987"
   transform -dir ./json_files \
             -workers 128 \
             -max-conns 512
   ```

4. **Handle rate limiting with backoff**  
   ```bash
   export OMNIPUB_API_KEY="abc123"
   transform -dir ./json_files \
             -workers 3 \
             -backoff 500 \
             -save-failures failed_uploads.txt
   ```

5. **Retry failed uploads**  
   ```bash
   export OMNIPUB_API_KEY="abc123"
   transform -retry failed_uploads.txt \
             -workers 1 \
             -backoff 1000 \
             -collection 69 \
             -save-failures remaining_failures.txt
   ```

After completion, you'll see a summary like:

```
Done. Success: 7980  Failure: 20
```

## Handling Rate Limiting

If you encounter `ENHANCE_YOUR_CALM` errors (HTTP/2 rate limiting), try these approaches:

1. **Reduce concurrent workers**: Use `-workers 1` or `-workers 2` to reduce concurrency
2. **Add backoff time**: Use `-backoff 1000` to add a 1-second pause between requests
3. **Save failures for later**: Use `-save-failures failed.txt` to record any remaining failures
4. **Retry separately**: Use `-retry failed.txt` to process only the failed files later

This approach allows for graceful handling of rate limiting by:
- Reducing concurrent requests
- Adding delays between requests
- Tracking failures for later retry
- Processing only specific files when needed

## License

This project is released under the [MIT License](LICENSE).