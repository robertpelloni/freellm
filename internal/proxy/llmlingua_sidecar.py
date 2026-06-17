import json
import logging
import sys
from llmlingua import PromptCompressor

# Try to use Flask, fallback to http.server if missing
try:
    from flask import Flask, request, jsonify
    HAS_FLASK = True
except ImportError:
    HAS_FLASK = False
    from http.server import HTTPServer, BaseHTTPRequestHandler

logging.basicConfig(level=logging.INFO)

# Initialize LLMLingua
# By default, use a small model for speed
try:
    compressor = PromptCompressor(
        model_name="microsoft/phi-2", # Good balance of speed/quality for compression
        device_map="auto"
    )
except Exception as e:
    logging.error(f"Failed to load LLMLingua: {e}")
    sys.exit(1)

if HAS_FLASK:
    app = Flask(__name__)

    @app.route('/compress', methods=['POST'])
    def compress():
        try:
            data = request.json
            if not data:
                return jsonify({"error": "No JSON body"}), 400
            
            # Extract messages
            messages = data.get("messages", [])
            if not messages:
                return jsonify(data) # No change
            
            full_text = ""
            for m in messages:
                full_text += f"{m.get('role', 'user')}: {m.get('content', '')}\n"
            
            compressed_result = compressor.compress_prompt(
                full_text,
                target_token=2000,
                condition_compare=True,
                condition_sep="\n",
                reorder_context="sort"
            )
            
            new_messages = [{"role": "user", "content": compressed_result["compressed_prompt"]}]
            data["messages"] = new_messages
            return jsonify(data)
        except Exception as e:
            logging.error(f"Compression error: {e}")
            return jsonify({"error": str(e)}), 500

    def run_server():
        app.run(port=7788)

else:
    class CompressionHandler(BaseHTTPRequestHandler):
        def do_POST(self):
            if self.path != '/compress':
                self.send_response(404)
                self.end_headers()
                return

            content_length = int(self.headers['Content-Length'])
            post_data = self.rfile.read(content_length)
            
            try:
                data = json.loads(post_data)
                messages = data.get("messages", [])
                if not messages:
                    response_body = json.dumps(data)
                else:
                    full_text = ""
                    for m in messages:
                        full_text += f"{m.get('role', 'user')}: {m.get('content', '')}\n"
                    
                    compressed_result = compressor.compress_prompt(
                        full_text,
                        target_token=2000,
                        condition_compare=True,
                        condition_sep="\n",
                        reorder_context="sort"
                    )
                    
                    new_messages = [{"role": "user", "content": compressed_result["compressed_prompt"]}]
                    data["messages"] = new_messages
                    response_body = json.dumps(data)
                
                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.end_headers()
                self.wfile.write(response_body.encode('utf-8'))
            except Exception as e:
                logging.error(f"Compression error: {e}")
                self.send_response(500)
                self.end_headers()
                self.wfile.write(str(e).encode('utf-8'))

    def run_server():
        server = HTTPServer(('127.0.0.1', 7788), CompressionHandler)
        logging.info("Starting LLMLingua sidecar via http.server on port 7788")
        server.serve_forever()

if __name__ == '__main__':
    run_server()
