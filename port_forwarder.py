import socket
import threading
import sys
import time

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

def handle_client(client_socket, target_port):
    target_socket = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    try:
        target_socket.connect(('127.0.0.1', target_port))
    except Exception as e:
        client_socket.close()
        return

    threading.Thread(target=forward, args=(client_socket, target_socket), daemon=True).start()
    threading.Thread(target=forward, args=(target_socket, client_socket), daemon=True).start()

def listen_and_forward(listen_port, target_port):
    server = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    try:
        server.bind(('127.0.0.1', listen_port))
    except Exception as e:
        print(f"Note: Could not bind to port {listen_port} (it may already be in use): {e}")
        return
        
    server.listen(100)
    print(f"Forwarder Active: {listen_port} -> {target_port}")
    try:
        while True:
            client, addr = server.accept()
            handle_client(client, target_port)
    except Exception:
        pass
    finally:
        server.close()

def main():
    t1 = threading.Thread(target=listen_and_forward, args=(11434, 1234), daemon=True)
    t2 = threading.Thread(target=listen_and_forward, args=(1234, 11434), daemon=True)
    
    t1.start()
    t2.start()
    
    print("Local Bidirectional Port Forwarders Running...")
    print("Press Ctrl+C to stop.")
    
    try:
        while True:
            time.sleep(1)
    except KeyboardInterrupt:
        print("\nStopping forwarders...")

if __name__ == '__main__':
    main()
