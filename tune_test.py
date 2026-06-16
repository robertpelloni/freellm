import requests
import time
import json

def test_proxy():
    url = "http://localhost:4000/v1/chat/completions"
    payload = {
        "model": "free-llm",
        "messages": [{"role": "user", "content": "What is the capital of France? Answer in one word."}],
        "stream": False
    }
    headers = {
        "Content-Type": "application/json",
        "Authorization": "Bearer sk-freellm"
    }

    print(f"Sending request to {url}...")
    start_time = time.time()
    try:
        response = requests.post(url, headers=headers, json=payload, timeout=60)
        end_time = time.time()
        latency = end_time - start_time
        print(f"Status: {response.status_code}")
        print(f"Latency: {latency:.2f}s")
        if response.status_code == 200:
            data = response.json()
            print(f"Response: {data['choices'][0]['message']['content']}")
            print(f"Model used: {data.get('model', 'unknown')}")
        else:
            print(f"Error: {response.text}")
    except Exception as e:
        print(f"Exception: {e}")

if __name__ == "__main__":
    test_proxy()
