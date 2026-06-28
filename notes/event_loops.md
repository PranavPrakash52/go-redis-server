# Building a Redis-style DB in Go - Notes

## 1. Handling Concurrent Clients: The Problem with Threads
The traditional approach to handling concurrent clients is to spawn a new thread for every client connection. However, this comes with significant drawbacks:
- **Thread Safety (Race Conditions):** Shared memory (like global variables) is vulnerable. Operations like `count++` are not atomic. Multiple threads modifying the same data can lead to inconsistent states unless explicitly protected by mutexes or semaphores, adding significant code complexity.
- **Blocking I/O (Wasted Resources):** When a thread waits for network I/O, it blocks. Even if the CPU has nothing else to do, threads remain stuck in a wait state just waiting for data to arrive, preventing true efficiency.
- **Context Switching Overhead:** Managing thousands of threads slows down the system.

## 2. The UNIX Philosophy: "Everything is a File"
You might wonder: *Why is everything in Linux a File Descriptor?*

In Unix/Linux, the designers created a unified, elegant abstraction: **treat all I/O resources as if they were regular files**. 
- A text document on your disk is a file.
- A network socket connected to a client is a file.
- A USB drive is a file.
- Even an in-memory buffer can be a file.

**Why do this?** Because it allows developers to use the exact same system calls (`read()`, `write()`, `close()`) to interact with entirely different hardware. A developer doesn't need to learn one API for writing to disk and a completely different API for sending data over the network. 

To track all these open "files", the kernel uses a **File Descriptor (FD)**, which is simply a non-negative integer (e.g., `0`, `1`, `2`, `3`) that serves as an index into a table maintained by the kernel for each process. When a client connects to our Redis server, the OS creates a new socket, assigns it an FD, and we use that integer to communicate with the client.

## 3. What is `epoll` and How Does It Work?
To support a massive number of concurrent clients on a **single thread** (which is how Redis, Node.js, and Python AsyncIO work), we use **I/O Multiplexing** via an Event Loop.

`epoll` is the Linux-specific system call interface for I/O multiplexing. Its job is incredibly simple but powerful: **it monitors a large number of File Descriptors to see if any of them are ready for I/O (reading or writing)**.

### The Three Core `epoll` Operations:
1. `epoll_create1`: Initializes an `epoll` instance in the kernel and returns a file descriptor pointing to this instance.
2. `epoll_ctl`: Used to manage the list of FDs you want to monitor. You register the main Server Socket (for incoming connections) and all connected Client Sockets with this command.
3. `epoll_wait`: A **blocking system call**. It pauses the execution of your single thread until *at least one* of the monitored file descriptors has data ready. When unblocked, it returns a list of only the FDs that are ready.

*Note: macOS/BSD use `kqueue` and Windows uses `IOCP` for this exact same mechanism.*

## 4. How Interrupts are Captured by `epoll`
How does `epoll` actually know when a socket has data without constantly asking the network card? It relies on hardware interrupts and the kernel buffer.

Here is the exact flow of data from the hardware to `epoll`:
1. **Hardware Arrival:** A client sends data over the network. It physically arrives at your server's Network Interface Card (NIC).
2. **Hardware Interrupt:** The NIC immediately sends an electrical signal (an interrupt) to the CPU. 
3. **Kernel Buffer Transfer:** The CPU stops whatever it was doing for a split second, and the OS Kernel copies the data from the NIC into a designated memory space in RAM called the **Kernel Buffer**.
4. **Marking FD Ready:** Because the Kernel manages the networking stack, it knows exactly which socket (and which File Descriptor) this incoming packet belongs to. It marks that specific FD as "ready for read" in its internal tables.
5. **Waking up `epoll_wait`:** If your application was paused on `epoll_wait()`, the Kernel immediately wakes up your process and hands it the list of FDs that were just marked as ready.
6. **User Space Copy:** Your application then calls `read()` on those ready sockets, which copies the data from the Kernel Buffer into your Application (User) Space so you can parse the Redis command.

Because `epoll` taps directly into the OS's interrupt handling and buffer management, it is incredibly efficient. It never wastes CPU cycles checking empty sockets.

## 5. The Event Loop Architecture
By combining all these concepts, a single-threaded Redis server runs an infinite loop:
1. Call `epoll_wait()`. The loop sleeps efficiently, doing 0% CPU work, until an interrupt signals data is ready.
2. Once woken up, iterate over the list of ready File Descriptors returned by the kernel.
3. For each FD, read the data from the kernel, parse the command, execute it, and write the response back.
4. Loop back to step 1.

## 6. Deep Dive: How the OS Manages File Descriptors
To understand how the kernel tracks all this internally and wakes up the correct processes, we have to look at the OS's internal data structures.

### Where are File Descriptors Stored?
When you start a process (like your Go Redis server), the Linux kernel creates a **Process Control Block (PCB)**, known internally as `task_struct`.
Inside this `task_struct`, there is a process-specific array called the **File Descriptor Table**. 
- A File Descriptor (FD) like `3`, `4`, or `5` is literally just an **index (array map)** into this specific table.
- Because the table is process-specific, FD `5` in Process A might point to a network socket, while FD `5` in Process B might point to a completely different text file.

### How Does the Address Map Work? (From FD to Hardware)
The mapping from your application's integer FD to the actual hardware happens in three layers of indirection:
1. **File Descriptor Table (Process-level):** Your FD integer is the index in this array. The array element holds a pointer to...
2. **Open File Table (System-wide):** This table is shared across the entire OS. It tracks the status of the open file, such as whether it was opened for reading or writing, and the current read/write offset. This entry points to...
3. **The Inode Table / Socket Structure (Kernel-level):** This is the actual representation of the underlying hardware or memory. For networking, it points to a `socket` data structure in the kernel memory, which holds the actual receive and send buffers where the network card places incoming data.

### How Does the OS Know Which Process to Wake Up?
The key insight is that **registration** (`epoll_ctl`) and **waiting** (`epoll_wait`) are two separate acts, and they touch **two different wait queues**. Crucially, it is *not* your process that gets attached to a socket — it is the `epoll` instance. Your process only enters a wait queue at the moment it actually calls `epoll_wait()`.

#### The Registration Step (`epoll_ctl`)
When you call `epoll_ctl(epfd, EPOLL_CTL_ADD, sock_fd, ...)`, the kernel:
1. Creates a small struct called an **`epitem`** that links your `epoll` instance (internally an `eventpoll` struct) to this specific socket.
2. Inserts an entry into the **socket's Wait Queue** (`sk_wq` in kernel terms). That entry carries a callback function called **`ep_poll_callback`**.

So what literally sits in the socket's wait queue is **not your process, and not the `epoll_fd` integer** — it is a callback entry that says: *"If data arrives here, fire `ep_poll_callback`, which knows which `epitem` / `eventpoll` I belong to."*

This registration is **persistent** and survives across many `epoll_wait` calls. If you register 1,000 sockets with one `epoll` instance, then all 1,000 sockets have a callback entry in their wait queue, all pointing back to the same `eventpoll`.

#### `epoll_event` (Your Code) vs `epitem` (The Kernel)
There is a common point of confusion here worth clearing up. In your Go code (see `server/async_tcp_linux.go`), you write something like:

```go
var socketServerEvent syscall.EpollEvent = syscall.EpollEvent{
    Events: syscall.EPOLLIN, // listen for read events on the server socket
    Fd:     int32(serverFD),
}
syscall.EpollCtl(epollFD, syscall.EPOLL_CTL_ADD, serverFD, &socketServerEvent)
```

You might wonder: *isn't this `epoll_event` the thing being added to the queue? Where is the callback?*

**No.** `epoll_event` is just a **message** you hand to the kernel — a small payload describing what you want. The kernel never puts your user-space struct into any queue. Instead, it reads your `epoll_event` and builds its own kernel-side object, the **`epitem`**. They look related but live in different worlds:

| | `syscall.EpollEvent` (your Go code) | `epitem` (kernel, you never see it) |
|---|---|---|
| Where it lives | User space | Kernel space |
| What it holds | `Events` (e.g. `EPOLLIN`), `Fd` | copy of those events + `data`, a link to the `eventpoll`, a link to the socket, **and the `ep_poll_callback` entry** |
| Who creates it | Your code | The kernel, *inside* the `epoll_ctl` syscall |

That is why you don't see a callback: **you cannot register one from user space.** The callback is a hardcoded kernel function (`ep_poll_callback`), identical for every `epoll` registration in every process. The kernel attaches it itself.

So is `epoll_event` "part of" the `epitem`? Only its **contents** are — the `Events` mask and the `Fd`/`data` field get **copied into** the `epitem`. After the syscall returns, your Go `socketServerEvent` is just a regular local variable; nothing in the kernel points back at your user-space memory. The `epitem` is the larger kernel object that carries those copied fields *plus* the callback and the linkage.

#### Why the `Fd` Field Matters
The `Fd: int32(serverFD)` you set is not just decoration — it becomes the **`data`** the kernel stores inside the `epitem`. Later, when `epoll_wait` wakes you up, the kernel fills your `events` array with fresh `epoll_event`s and echoes that `Fd` back, so your loop knows exactly which connection to `read()`:

```go
fd := events[i].Fd   // "ah, this FD has data ready"
```

That is the full round-trip: you hand the kernel `Fd` at registration → it is stored in the `epitem` → it is handed back to you at wake-up.

#### The Waiting Step (`epoll_wait`)
When your process calls `epoll_wait()`, *that* is the moment your process gets added to a wait queue — but a **different one**: the `eventpoll`'s own wait queue (`eventpoll->wq`). Your process goes to sleep there.

#### The Wake-Up Chain
Now the full flow when a packet arrives:

1. **Hardware Interrupt:** Data arrives at the NIC, triggering an interrupt.
2. **Kernel Buffer Copy:** The kernel copies the data into the socket's receive buffer.
3. **Fire Callback:** The kernel walks the socket's Wait Queue and invokes `ep_poll_callback`.
4. **Update Ready List:** `ep_poll_callback` adds the relevant `epitem` to the `eventpoll`'s **Ready List** (`rdllist`).
5. **Wake the Process:** `ep_poll_callback` also wakes up any process sleeping on `eventpoll->wq`, changing its state from "sleeping" to "runnable".
6. **Return:** The OS scheduler gives your process CPU time; `epoll_wait()` unblocks and hands you the specific FDs that triggered the wake-up so you can read them.

Notice the clean separation of concerns: the socket only knows "someone cares about me" (via the callback), and the `eventpoll` is what actually tracks which process to wake and which FDs are ready.

## 7. Architectural Design: One `epoll` vs. Multiple `epoll`s
A common architectural question arises when dealing with thousands of connections: *Should we use one `epoll` instance for everything, or two separate instances (one for the Server Socket to accept connections, and one for the Client Sockets to handle data)?*

The answer depends entirely on your threading model:

### Scenario A: Strictly Single-Threaded Server (Classic Redis, Node.js)
If your entire event loop runs on a single thread, **you must use only one `epoll` instance**.
- **Why?** Because `epoll_wait()` is a blocking system call. If your single thread is asleep waiting on the "Client epoll", it physically cannot monitor the "Server Socket epoll" at the same time.
- **The Solution:** You register the 1 main Server Socket AND all 1,000+ Client Sockets into the exact same `epoll` instance. `epoll_wait()` will wake up the thread if *any* of them are ready. This is how Redis originally solved the concurrency problem without locking.

### Scenario B: Multi-Threaded Server (The "Multi-Reactor" Pattern)
If you are building a multi-threaded server (like Nginx, Memcached, or modern Redis 6+), using multiple `epoll` instances is the **gold standard for high performance**. This is often called the Boss-Worker or Multi-Reactor pattern.
- **The Boss Thread (1 epoll):** A dedicated thread runs an `epoll` instance that *only* monitors the Server Socket. Its sole job is to wake up, call `accept()`, get the new client FD, and pass it to a worker.
- **The Worker Threads (N epolls):** You have a pool of worker threads (e.g., matching your CPU cores). Each worker runs its own `epoll` instance. The Boss thread distributes the thousands of client connections evenly across these worker `epoll`s.
- **Why?** If multiple threads shared a single `epoll`, they would fight over kernel locks to read the event list. By giving each thread its own `epoll`, they run entirely independently at maximum CPU speed.

*For a pure educational Redis clone in Go, starting with a single-threaded, single-`epoll` architecture is the best way to understand the core mechanics without dealing with race conditions.*

## 8. Memory Management: The `read()` System Call and Virtual Memory
When `epoll` tells us a socket is ready, our application calls the `read()` system call to fetch the data. But what actually happens to the data in memory?

### The Kernel-to-User Space Copy
For security and stability, an application (running in User Space) is strictly forbidden from directly accessing the OS Kernel's memory.
When you call `read()` in Go, you pass it a buffer:
```go
buf := make([]byte, 1024) // Created in your process's User Space
bytesRead, err := syscall.Read(clientFD, buf)
```
The `read()` system call acts as a bridge. It instructs the Kernel: *"Please take the incoming network data from your protected Kernel Buffer and safely copy it into this specific `buf` array in my Application's memory."*

### The Role of Virtual Memory
The `buf` array resides in your process's memory, and every single memory address your application interacts with is a **Virtual Memory Address**.
- Your process behaves as if it has a huge, continuous, private block of memory.
- Behind the scenes, the OS and the CPU's Memory Management Unit (MMU) use **Page Tables** to map those virtual addresses to the actual physical RAM (or swap space).

When the kernel performs the copy from its Kernel Buffer to your Application Buffer, it is writing across the boundary into your mapped virtual memory space.

*Performance Note: This exact Kernel-to-User memory copy is a known performance bottleneck in ultra-high-speed networking. For extreme performance (e.g., 100Gbps connections), engineers use "Zero-Copy" techniques (like `sendfile` or `io_uring`) to bypass this memory copy entirely!*

## 9. Advanced Internals: The "Thundering Herd" Problem
Because a socket's Wait Queue is just a list, it is possible for multiple processes (or multiple threads, each with their own `epoll` instance) to monitor the exact same Server Socket simultaneously.

**The Problem:** 
If 4 processes are waiting in the Wait Queue for a Server Socket, and a single client connects, the Kernel will wake up **all 4 processes**. All 4 will rush to call `accept()`, but only one will succeed. The other 3 will fail with an `EAGAIN` (Resource temporarily unavailable) error and go back to sleep. This wastes significant CPU cycles and is known as the "Thundering Herd" problem.

**The Solution:**
Modern Linux kernels introduced the `EPOLLEXCLUSIVE` flag. When you attach an `epoll` to a socket with this flag, you instruct the kernel to only wake up **one** exclusive waiter from the Wait Queue, completely solving the Thundering Herd issue. This is how high-performance multi-process servers like Nginx operate.

## 10. Advanced Internals: The `epoll` Ready List vs. Socket Wait Queues
It is critical to distinguish between the **three** different queues involved in I/O Multiplexing:
1. **The Socket Wait Queue (`sk_wq`, belongs to each socket):** This keeps track of *who* is interested in this socket. Its entries are **callback entries** (`ep_poll_callback`), one per `epoll` instance monitoring the socket — not raw processes and not the `epoll_fd` integer.
2. **The Eventpoll Wait Queue (`eventpoll->wq`, belongs to the `epoll` instance):** This holds the **process(es)** that are currently sleeping inside `epoll_wait()`.
3. **The Ready List (`eventpoll->rdllist`, belongs to the `epoll` instance):** This keeps track of *which File Descriptors* currently have data ready to be read.

### The True Flow of an Interrupt
What happens if your application is awake and processing 5 File Descriptors, and suddenly data arrives for a 6th File Descriptor?

1. **Hardware Preemption:** The Network Interface Card (NIC) sends an electrical signal to the CPU (a hardware interrupt). The CPU physically stops executing your User Space application and jumps into the Kernel's Interrupt Service Routine (ISR). *(If you have multiple cores, this interrupt might be handled on a different core while your app continues running uninterrupted).*
2. **Updating the Ready List:** The Kernel reads the data from the NIC. It checks the socket's **Wait Queue** and sees your `epoll` instance is registered there. The Kernel then silently adds this 6th FD to your `epoll` instance's internal **Ready List** in Kernel memory.
3. **Resuming the Application:** The CPU finishes the interrupt handling and resumes your Application. Your application is completely unaware it was paused.
4. **The Snapshot:** Your application does *not* instantly see the 6th FD. When `epoll_wait()` returned the original 5 FDs, it returned a **static snapshot** (an array). Your application will only see the 6th FD the *next time* the infinite loop spins around and calls `epoll_wait()` again.

**The Golden Rule of Event Loops:** Because of this static snapshot behavior, you must **never block the event loop**. If processing a single FD takes 5 seconds, the Kernel will quietly stack up hundreds of new FDs in the Ready List, but your application will be completely blind to them until those 5 seconds are up and `epoll_wait()` is called again.
