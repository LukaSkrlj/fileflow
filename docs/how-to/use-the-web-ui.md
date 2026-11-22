# How to watch uploads, downloads and streaming in the browser

The `web/` folder holds an Angular app that shows fileflow transfers live: progress, current and average speed, a speed chart, time to first byte, and the trace id and `Server-Timing` of every request.

## Start it

You need **Node 24.15 or newer** (Angular 22 doesn't support Node 25). With nvm:

```bash
cd web && nvm install && nvm use     # reads web/.nvmrc
```

Start fileflow on `:8080` (any backend), then the UI:

```bash
make web            # or: cd web && npm install && npm start
```

Open <http://localhost:4200>. The dev server forwards `/v1` and `/readyz` to `localhost:8080` (`web/proxy.conf.json`), so fileflow needs no CORS settings. To point it at another server, change `target` in that file.

## What each action shows

| Action | What happens | What to look at |
|---|---|---|
| **Drop files / choose files** | `PUT` with upload progress, sent straight from disk | Speed while sending. At 100 % the bar waits for the server to verify the checksum and commit. Then you get the `sha256` and trace id. |
| **Stream** | Reads the file through a `fetch` stream and counts the bytes. Nothing is saved. | Real download speed, time to first byte, and the server's `backend_stat` / `backend_open` from `Server-Timing` |
| **Save** | A plain browser download | The browser's own download bar. It writes to disk while it downloads. |
| **Play** (video, audio, image) | The browser plays the file from fileflow | Seek in a video: the browser sends `Range` requests (`206` in devtools → Network), so only the part you jump to is downloaded. |
| **Cancel** | Aborts the request | The server aborts its multipart upload, and nothing becomes visible |

Requests are sent with `X-Client-Kind: human`, so they show up as `client=human` in fileflow's logs and metrics. Copy a trace id from a finished transfer to find its log line (see [trace a request](trace-a-request.md)).

## Build for production

```bash
cd web && npm run build      # output in web/dist/web/browser
```

Serve those files from the same origin as fileflow (for example, behind the same ingress, with `/v1` routed to fileflow). fileflow doesn't send CORS headers, so a UI on another origin can't call it.
