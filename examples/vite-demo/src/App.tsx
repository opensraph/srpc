import { useState, useEffect } from 'react';
import reactLogo from '@/assets/react.svg';
import viteLogo from '/vite.svg';
import './App.css';

import { createClient } from "@connectrpc/connect";
import { createGrpcWebTransport } from "@connectrpc/connect-web";
import { EchoService } from '@vitedemo/proto/srpc/echo/v1';

const fallbackToken = "some-secret-token";



function App() {
  const [count, setCount] = useState(0);
  const [message, setMessage] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    const transport = createGrpcWebTransport({
      baseUrl: "https://localhost:50051",
      useBinaryFormat: true,
      interceptors: [
        (next) => async (req) => {
          req.header.set("Authorization", `Bearer ${fallbackToken}`);
          // console.log("Request:", req);
          try {
            const res = await next(req);
            // console.log("Response:", res);
            return res;
          } catch (err) {
            console.error("Detailed error:", err);
            throw err;
          }
        },
      ],
    });

    const client = createClient(EchoService, transport);

    const unaryEcho = async () => {
      // Call the unary method
      console.log('Calling unaryEcho...');
      const response = await client.unaryEcho({
        message: 'Hello from Vite + React + connect-web',
      });
      console.log('Response:', response.message);
      setMessage(response.message);
    };

    const streamRequest = async function* () {
      for (const message of ["Hello ", "from ", "Vite ", "+ ", "React ", "+ ", "connect-web"]) {
        yield {
          message,
        };
      }
    };

    const clientStreamingEcho = async () => {
      // Call the client streaming method
      console.log('Calling clientStreamingEcho...');
      const clientStreamingResponse = await client.clientStreamingEcho(streamRequest());
      console.log('Client Streaming Response:', clientStreamingResponse.message);
    };

    const serverStreamingEcho = async () => {
      // Call the server streaming method
      console.log('Calling serverStreamingEcho...');
      const serverStreamingResponse = client.serverStreamingEcho({
        message: 'Hello from Vite + React + connect-web',
      });
      for await (const response of serverStreamingResponse) {
        console.log('Server Streaming Response:', response.message);
      }
    };

    const bidirectionalStreamingEcho = async () => {
      // Call the bidirectional streaming method
      console.log('Calling bidirectionalStreamingEcho...');
      const bidirectionalStreamingResponse = client.bidirectionalStreamingEcho(streamRequest());
      for await (const response of bidirectionalStreamingResponse) {
        console.log('Bidirectional Streaming Response:', response.message);
      }
    };

    setLoading(true);
    setError('');
    try {
      console.log('Starting gRPC calls...');
      unaryEcho();
      clientStreamingEcho();
      serverStreamingEcho();
      bidirectionalStreamingEcho();
    }
    catch (err: unknown) {
      console.error("Error:", err);
      setError(`Error: ${err}`);
    }
    finally {
      setLoading(false);
    }
  }, []);

  return (
    <>
      <div>
        <a href="https://vitejs.dev" target="_blank" rel="noreferrer">
          <img src={viteLogo} className="logo" alt="Vite logo" />
        </a>
        <a href="https://react.dev" target="_blank" rel="noreferrer">
          <img src={reactLogo} className="logo react" alt="React logo" />
        </a>
      </div>
      <h1>Vite + React + gRPC-web</h1>
      <div className="card">
        <button onClick={() => setCount((count) => count + 1)}>
          count is {count}
        </button>

        {loading && <p>Loading...</p>}
        {error && <p className="error">Error: {error}</p>}
        {message && <p className="message">Server says: {message}</p>}

        <p>
          Edit <code>src/App.tsx</code> and save to test HMR
        </p>
      </div>
    </>
  );
}

export default App;