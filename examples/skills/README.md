# Skills

AX has built-in support for [Agent Skills](https://agentskills.io).

## Configuration

To enable skills with a custom directory, you can set the `skills_dir` in your `ax.yaml` file:

```yaml
planner:
  gemini:
    model: "gemini-3-flash-preview"
    timeout: "60s"
    skills_dir: "./examples/skills"
```

If you omit `skills_dir` in your `ax.yaml` file, AX will check the `SKILLS_DIR` environment variable first, and finally fall back to `~/.agents/skills`.

## Example: Using the `emoji` Skill

AX contains a sample `emoji` skill in `examples/skills/emoji`. If you run a task that matches the description of the skill, the planner will automatically activate it and execute its scripts!

Run the following command in your terminal:

```bash
ax exec --input "Give me an emoji for happy"
```

The planner will:
1. Discover the `emoji` skill.
2. Determine that your query matches its description.
3. Call `activate_skill` to load the instructions.
4. Execute the `emoji` skill via text generation or script execution based on its instructions.

### Custom Skills

Refer to the [Agent Skills](https://agentskills.io) documentation for more information on how to create custom skills.