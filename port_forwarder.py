import socket
import threading
import sys

def forward(src, dst):
    try:
        while True:
            data = src.recv(4096)
            if not data:
                break
            dst.sendall(data)
    except Exception:
        pass
    finally:
        src.close()
        dst.close()

def handle_client(client_socket):
    target_socket = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    try:
        target_socket.connect(('127.0.0.1', 1234))
    except Exception as e:
        client_socket.close()
        return

    threading.Thread(target=forward, args=(client_socket, target_socket), daemon=True).start()
    threading.Thread(target=forward, args=(target_socket, client_socket), daemon=True).start()

def main():
    server = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    try:
        server.bind(('127.0.0.1', 11434))
    except Exception as e:
        print(f"Failed to bind to port 11434: {e}")
        sys.exit(1)
        
    server.listen(100)
    print("Local TCP Port Forwarder Active: 11434 -> 1234")
    try:
        while True:
            client, addr = server.accept()
            handle_client(client)
    except KeyboardInterrupt:
        print("Stopping forwarder...")
    finally:
        server.close()

if __name__ == '__main__':
    main()
