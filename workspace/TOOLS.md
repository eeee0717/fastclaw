# Tools Reference

## Built-in Tools

### exec
Execute shell commands with timeout and safety checks.
- Dangerous commands (rm -rf /, mkfs, etc.) are blocked
- Default timeout: 30 seconds

### read_file
Read file contents. Supports absolute paths or paths relative to the workspace.

### write_file
Write content to a file. Creates parent directories as needed.

### list_dir
List files and directories with size information.

### message
Send text, local image attachments, or local files to the current conversation or a specific route.
- Prefer this tool when the user asks you to send a message, local image, or local file to the current chat.
- `media_paths` accepts local image file paths.
- `file_paths` accepts local file paths to send as documents.
- If `channel`, `account_id`, or `chat_id` are omitted, use the current conversation.
- Put any user-facing text in `text`; when sending images or files, it will be used as the caption/message when supported.
