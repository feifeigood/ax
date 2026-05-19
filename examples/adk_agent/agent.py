# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

from google.adk.agents import Agent

def get_weather(city: str) -> dict:
    """Retrieves the current weather report for a specified city.

    Args:
        city (str): The name of the city for which to retrieve the weather report.

    Returns:
        dict: status and result or error msg.
    """
    if "new york" in city.lower() or "nyc" in city.lower():
        return {
            "status": "success",
            "report": (
                "The weather in New York is sunny with a temperature of 25 degrees"
                " Celsius (77 degrees Fahrenheit)."
            ),
        }
    elif "san francisco" in city.lower() or "sf" in city.lower():
        return {
            "status": "success",
            "report": (
                "The weather in San Francisco is foggy with a temperature of 16 degrees"
                " Celsius (60.8 degrees Fahrenheit)."
            ),
        }
    else:
        return {
            "status": "error",
            "error_message": f"Weather information for '{city}' is not available.",
        }


root_agent = Agent(
    name="weather_agent",
    model="gemini-3.5-flash",
    description=(
        "Agent to answer questions about the weather in a city."
    ),
    instruction=(
        "You are a helpful agent who can answer user questions about the weather in a city."
    ),
    tools=[get_weather],
)
