# Running the Application

This guide contains instructions on how to run, configure, and test the `relay` file transfer application locally and in production.

---

## 1. Prerequisites
Ensure you have the following installed on your machine:
*   **Go**: Version `1.22` or higher
*   **Node.js**: Version `20` or higher
*   **npm**: Version `10` or higher

---

## 2. Local Development Setup

To run the application locally, you will need to start both the Go backend and the React frontend.

### Step A: Start the Go Backend
1. Open a terminal and navigate to the `backend/` directory:
    ```powershell
    cd backend
    ```
2. Fetch dependencies:
    ```powershell
    go mod tidy
    ```
3. Set the frontend URL environment variable (so the Go server allows CORS and frames connection links):
    ```powershell
    $env:PUBLIC_BASE_URL="http://localhost:5173"
    ```
    *(For Linux/macOS, use: `export PUBLIC_BASE_URL="http://localhost:5173"`)*
4. Run the backend server:
    ```powershell
    go run ./cmd/server
    ```
    *The backend server will start listening at `http://localhost:8080`.*

### Step B: Start the React Frontend
1. Open a second terminal and navigate to the `frontend/` directory:
    ```powershell
    cd frontend
    ```
2. Install dependencies:
    ```powershell
    npm install
    ```
3. Run the Vite development server:
    ```powershell
    npm run dev
    ```
    *The frontend application will start running at `http://localhost:5173`.*

---

## 3. Configuration & Environment Variables

You can configure both the backend and frontend using environment variables.

### Frontend Environment Variables (`frontend/.env` or `.env.production`)
*   `VITE_API_URL`: The HTTP URL of the Go backend (e.g. `http://localhost:8080`).
*   `VITE_WS_URL`: The WebSocket URL of the Go backend (e.g. `ws://localhost:8080`).
*   `VITE_WEBRTC_ICE_SERVERS`: A JSON array of RTCConfiguration ICE servers (STUN/TURN) to use. Defaults to:
    ```json
    [
      { "urls": "stun:stun.l.google.com:19302" },
      { "urls": "stun:stun1.l.google.com:19302" }
    ]
    ```
*   `VITE_WEBRTC_CONNECTION_TIMEOUT`: The duration in milliseconds to wait for a WebRTC connection handshake before automatically falling back to Server Relay. Defaults to `10000` (10 seconds).

### Backend Environment Variables (`backend/` command line)
*   `PORT`: Port to listen on (defaults to `8080`).
*   `ALLOWED_ORIGINS`: Comma-separated list of allowed CORS origins (defaults to `http://localhost:5173`).
*   `PUBLIC_BASE_URL`: Public URL used to prefix generated transfer links.
*   `TRANSFER_TTL`: Session duration in-memory (defaults to `15m`).

---

## 4. Running Tests

### Unit Tests
*   **Backend Go Tests**:
    Navigate to the `backend/` directory and run:
    ```powershell
    go test ./...
    ```
*   **Frontend Unit Tests**:
    Navigate to the `frontend/` directory and run:
    ```powershell
    npm run test
    ```

### Playwright End-to-End (E2E) Tests
1. Navigate to the `frontend/` directory.
2. Build the production build of the frontend:
    ```powershell
    npm run build
    ```
3. Run the Playwright test suite (this automatically spins up the Go server, Vite host, and opens a headless Chromium runner to perform full transfers):
    ```powershell
    npm run test:e2e
    ```

---

## 5. WebRTC Peer-to-Peer vs Server Relay Modes

Users can toggle their preferred transport mode in the settings panel on the Home page:
1.  **Automatic (Recommended)**: Attempts WebRTC Peer-to-Peer first. If the NAT traversal handshake times out (due to strict firewalls) or encounters a connection error, it gracefully falls back to **Server Relay** within 10 seconds.
2.  **Peer-to-Peer**: Restricts communication purely to WebRTC direct data channels. If NAT traversal fails, the transfer fails. No file bytes traverse the Go server.
3.  **Server Relay**: Bypasses WebRTC entirely and routes file chunks directly through the Go WebSocket relay.
