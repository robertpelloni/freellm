#!/usr/bin/env python3
"""
FreeLLM A2A Swarm Harness
=========================
Orchestrate giant swarms of A2A agents through the FreeLLM proxy.

Usage:
    # Register remote A2A agents with the swarm
    python freellm_swarm.py register http://localhost:5001
    python freellm_swarm.py register http://localhost:5002

    # Send a task to the best available agent
    python freellm_swarm.py dispatch "Write a Python function to sort a list" --skill llm-code

    # Broadcast a task to ALL agents
    python freellm_swarm.py broadcast "What is the capital of France?" --skill llm-chat

    # Fan-out different subtasks to different skills
    python freellm_swarm.py fanout '{"llm-chat": "Summarize this topic", "llm-code": "Write a test"}'

    # List all agents in the swarm
    python freellm_swarm.py list

    # Spawn a local A2A agent (starts an agent process)
    python freellm_swarm.py spawn --name "code-reviewer" --skill llm-code --count 5

    # Run a massive parallel swarm
    python freellm_swarm.py swarm --task "Review this code" --agents 20 --skill llm-code
"""

import argparse
import asyncio
import json
import os
import sys
import uuid
import httpx
import subprocess
from typing import Optional

# FreeLLM proxy base URL
FREELLM_URL = os.environ.get("FREELLM_URL", "http://localhost:4000")


async def a2a_send_message(base_url: str, message: str, message_id: Optional[str] = None) -> dict:
    """Send a message to an A2A agent via JSON-RPC 2.0."""
    if message_id is None:
        message_id = f"msg-{uuid.uuid4().hex[:8]}"

    payload = {
        "jsonrpc": "2.0",
        "id": 1,
        "method": "message/send",
        "params": {
            "message": {
                "kind": "message",
                "messageId": message_id,
                "role": "user",
                "parts": [{"kind": "text", "text": message}]
            }
        }
    }

    async with httpx.AsyncClient(timeout=120.0) as client:
        resp = await client.post(f"{base_url}/a2a", json=payload)
        return resp.json()


async def register_agent(swarm_url: str, agent_url: str) -> dict:
    """Register a remote A2A agent with the swarm coordinator."""
    # For now, this is done via the FreeLLM API
    # In production, this would call the swarm coordinator's REST API
    async with httpx.AsyncClient(timeout=10.0) as client:
        # First, resolve the agent card
        resp = await client.get(f"{agent_url}/.well-known/agent-card")
        card = resp.json()
        print(f"  Agent: {card.get('name', 'unknown')}")
        print(f"  URL: {agent_url}")
        print(f"  Skills: {[s['id'] for s in card.get('skills', [])]}")
        return card


async def dispatch_task(url: str, message: str, skill: str = "llm-chat") -> str:
    """Send a task to an A2A agent."""
    result = await a2a_send_message(url, message)
    
    if "error" in result:
        return f"ERROR: {result['error'].get('message', 'unknown error')}"
    
    task = result.get("result", {})
    artifacts = task.get("artifacts", [])
    
    texts = []
    for artifact in artifacts:
        for part in artifact.get("parts", []):
            if part.get("kind") == "text":
                texts.append(part.get("text", ""))
    
    return "\n".join(texts) if texts else json.dumps(task, indent=2)[:500]


async def broadcast_task(url: str, message: str, skill: str = "llm-chat") -> dict:
    """Broadcast a task to all agents."""
    result = await a2a_send_message(url, message)
    
    if "error" in result:
        return {"error": result["error"].get("message", "unknown error")}
    
    task = result.get("result", {})
    artifacts = task.get("artifacts", [])
    
    texts = []
    for artifact in artifacts:
        for part in artifact.get("parts", []):
            if part.get("kind") == "text":
                texts.append(part.get("text", ""))
    
    return {"response": "\n".join(texts), "task_id": task.get("id"), "state": task.get("status", {}).get("state")}


async def swarm_parallel(url: str, task: str, count: int, skill: str = "llm-chat") -> list:
    """Run a massive parallel swarm - sends the same task to the A2A server
    multiple times concurrently, each going through FreeLLM's routing to
    potentially different model providers."""
    print(f"🚀 Launching swarm: {count} concurrent agents")
    print(f"   Task: {task[:100]}...")
    print(f"   Skill: {skill}")
    print()
    
    tasks = []
    for i in range(count):
        msg_id = f"swarm-{i:04d}-{uuid.uuid4().hex[:6]}"
        tasks.append((i, a2a_send_message(url, task, msg_id)))
    
    results = []
    completed = 0
    failed = 0
    
    for coro in asyncio.as_completed([t[1] for t in tasks]):
        try:
            result = await coro
            if "error" in result:
                failed += 1
                results.append({"status": "error", "error": result["error"].get("message", "unknown")})
            else:
                completed += 1
                task_data = result.get("result", {})
                texts = []
                for artifact in task_data.get("artifacts", []):
                    for part in artifact.get("parts", []):
                        if part.get("kind") == "text":
                            texts.append(part.get("text", ""))
                results.append({
                    "status": "ok",
                    "task_id": task_data.get("id"),
                    "model": task_data.get("status", {}).get("message", {}).get("parts", [{}])[0].get("text", ""),
                    "response": "\n".join(texts)[:500],
                })
            print(f"  ✅ {completed} completed, ❌ {failed} failed ({completed+failed}/{count})")
        except Exception as e:
            failed += 1
            results.append({"status": "error", "error": str(e)})
            print(f"  ✅ {completed} completed, ❌ {failed} failed ({completed+failed}/{count})")
    
    print()
    print(f"Swarm complete: {completed}/{count} succeeded")
    return results


def spawn_agent_process(name: str, skill: str, port: int) -> subprocess.Popen:
    """Spawn a local A2A agent process that routes through FreeLLM.
    
    This creates a simple A2A server that uses FreeLLM for LLM calls.
    Each spawned agent is a separate process listening on its own port.
    """
    # Create a minimal A2A agent script
    agent_script = f'''#!/usr/bin/env python3
"""A2A Agent: {name}"""
import json
import uuid
from http.server import HTTPServer, BaseHTTPRequestHandler
import httpx

FREELLM_URL = "{FREELLM_URL}"

class AgentHandler(BaseHTTPRequestHandler):
    def do_OPTIONS(self):
        self.send_response(200)
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
        self.send_header("Access-Control-Allow-Headers", "Content-Type")
        self.end_headers()

    def do_GET(self):
        if self.path == "/.well-known/agent-card":
            card = {{
                "name": "{name}",
                "url": f"http://localhost:{port}/a2a",
                "version": "1.0.0",
                "protocolVersion": "0.3",
                "capabilities": {{"streaming": False, "pushNotifications": False, "stateTransitionHistory": False}},
                "defaultInputModes": ["text/plain"],
                "defaultOutputModes": ["text/plain"],
                "skills": [{{
                    "id": "{skill}",
                    "name": "{skill}",
                    "description": "Agent {name} with skill {skill}",
                    "tags": ["{skill}"]
                }}]
            }}
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Access-Control-Allow-Origin", "*")
            self.end_headers()
            self.wfile.write(json.dumps(card).encode())
        else:
            self.send_response(404)
            self.end_headers()

    def do_POST(self):
        if self.path == "/a2a":
            content_length = int(self.headers.get("Content-Length", 0))
            body = self.rfile.read(content_length)
            request = json.loads(body)
            
            # Extract message text
            message = request.get("params", {{}}).get("message", {{}})
            parts = message.get("parts", [])
            user_text = ""
            for part in parts:
                if part.get("kind") == "text":
                    user_text += part.get("text", "")
            
            # Route through FreeLLM
            try:
                resp = httpx.post(f"{{FREELLM_URL}}/v1/chat/completions", json={{
                    "model": "free-llm",
                    "messages": [{{"role": "user", "content": user_text}}],
                    "max_tokens": 1024
                }}, headers={{
                    "Content-Type": "application/json",
                    "Authorization": "Bearer sk-freellm",
                    "X-FreeLLM-Priority": "high"
                }}, timeout=60.0)
                
                data = resp.json()
                llm_text = data.get("choices", [{{}}])[0].get("message", {{}}).get("content", "No response")
                model = data.get("model", "unknown")
                
                # Build A2A response
                task_id = str(uuid.uuid4())
                artifact_id = str(uuid.uuid4())
                context_id = str(uuid.uuid4())
                
                response = {{
                    "jsonrpc": "2.0",
                    "id": request.get("id", 1),
                    "result": {{
                        "kind": "task",
                        "id": task_id,
                        "contextId": context_id,
                        "status": {{"state": "completed"}},
                        "artifacts": [{{
                            "artifactId": artifact_id,
                            "name": "llm-response",
                            "parts": [{{"kind": "text", "text": llm_text}}]
                        }}],
                        "history": [message]
                    }}
                }}
            except Exception as e:
                response = {{
                    "jsonrpc": "2.0",
                    "id": request.get("id", 1),
                    "error": {{"code": -32603, "message": str(e)}}
                }}
            
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Access-Control-Allow-Origin", "*")
            self.end_headers()
            self.wfile.write(json.dumps(response).encode())

if __name__ == "__main__":
    server = HTTPServer(("0.0.0.0", {port}), AgentHandler)
    print(f"[{name}] A2A agent listening on port {port}")
    server.serve_forever()
'''
    
    # Write script to temp file
    script_path = f"/tmp/freellm_agent_{name}_{port}.py"
    with open(script_path, "w") as f:
        f.write(agent_script)
    
    # Start the process
    proc = subprocess.Popen(
        [sys.executable, script_path],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        creationflags=subprocess.CREATE_NEW_PROCESS_GROUP if sys.platform == "win32" else 0
    )
    
    return proc


async def main():
    parser = argparse.ArgumentParser(description="FreeLLM A2A Swarm Harness")
    parser.add_argument("command", choices=[
        "dispatch", "broadcast", "fanout", "list", "spawn", "swarm", "register"
    ])
    parser.add_argument("message", nargs="?", default="Hello!")
    parser.add_argument("--skill", default="llm-chat", help="A2A skill ID")
    parser.add_argument("--url", default=FREELLM_URL, help="FreeLLM proxy URL")
    parser.add_argument("--agents", type=int, default=5, help="Number of swarm agents")
    parser.add_argument("--name", default="agent", help="Agent name for spawning")
    parser.add_argument("--count", type=int, default=1, help="Number of agents to spawn")
    parser.add_argument("--port", type=int, default=5001, help="Starting port for spawned agents")
    
    args = parser.parse_args()
    
    if args.command == "dispatch":
        print(f"📤 Dispatching task to {args.url} (skill: {args.skill})")
        result = await dispatch_task(args.url, args.message, args.skill)
        print(f"\n{result}")
    
    elif args.command == "broadcast":
        print(f"📢 Broadcasting to {args.url} (skill: {args.skill})")
        result = await broadcast_task(args.url, args.message, args.skill)
        print(json.dumps(result, indent=2))
    
    elif args.command == "fanout":
        subtasks = json.loads(args.message)
        print(f"🔀 Fan-out: {len(subtasks)} subtasks")
        results = {}
        for skill_id, msg in subtasks.items():
            print(f"  → {skill_id}: {msg[:60]}...")
            result = await dispatch_task(args.url, msg, skill_id)
            results[skill_id] = result
        print(json.dumps(results, indent=2))
    
    elif args.command == "list":
        print(f"📋 Listing agents at {args.url}")
        async with httpx.AsyncClient() as client:
            resp = await client.get(f"{args.url}/a2a/agents")
            agents = resp.json()
            for i, agent in enumerate(agents):
                print(f"  {i+1}. {agent.get('name', 'unknown')} @ {agent.get('url', '?')}")
                for skill in agent.get("skills", []):
                    print(f"     - {skill.get('id')}: {skill.get('name')}")
    
    elif args.command == "register":
        print(f"🔗 Registering agent: {args.message}")
        card = await register_agent(args.url, args.message)
        print(f"  ✅ Registered: {card.get('name')}")
    
    elif args.command == "spawn":
        print(f"🥚 Spawning {args.count} agent(s): {args.name}")
        processes = []
        for i in range(args.count):
            port = args.port + i
            name = f"{args.name}-{i}" if args.count > 1 else args.name
            print(f"  Starting {name} on port {port}...")
            proc = spawn_agent_process(name, args.skill, port)
            processes.append((name, port, proc))
            print(f"  ✅ {name} PID={proc.pid} on port {port}")
        
        print(f"\n{len(processes)} agent(s) running. Press Ctrl+C to stop.")
        try:
            for name, port, proc in processes:
                proc.wait()
        except KeyboardInterrupt:
            print("\nStopping agents...")
            for name, port, proc in processes:
                proc.terminate()
    
    elif args.command == "swarm":
        print(f"🐝 SWARM MODE: {args.agents} concurrent requests")
        results = await swarm_parallel(args.url, args.message, args.agents, args.skill)
        
        # Save results
        output_file = f"swarm_results_{uuid.uuid4().hex[:6]}.json"
        with open(output_file, "w") as f:
            json.dump(results, f, indent=2)
        print(f"\nResults saved to {output_file}")
        
        # Print summary
        succeeded = sum(1 for r in results if r.get("status") == "ok")
        print(f"Summary: {succeeded}/{len(results)} succeeded")


if __name__ == "__main__":
    asyncio.run(main())
