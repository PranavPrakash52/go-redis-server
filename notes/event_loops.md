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

#### Inside the Kernel: What Exactly *Is* an `eventpoll`?
A natural question at this point: *is `eventpoll` a queue? Is there one per socket? Where does it live?* All three have answers that are easy to get backwards, so let's pin them down.

**`eventpoll` is a `struct`, not a queue.** It is a plain C `struct` defined in the kernel (`fs/eventpoll.c`). It is the kernel-side object that represents *one* `epoll` instance. It *contains* several queues/lists as fields inside it, but it is not itself a queue.

**There is exactly ONE `eventpoll` per `epoll` instance — not per socket.** This is the part that is easy to invert. The per-socket object is the **`epitem`**, not the `eventpoll`. The cardinality is:

```
epoll_create1()  ──►  exactly ONE  struct eventpoll

epoll_ctl(ADD)   ──►  exactly ONE  struct epitem  per (epoll_instance, fd)
                        └─► inserted into that eventpoll's rbr (RB-tree)
                        └─► also gets a callback entry pushed into the socket's sk_wq
```

So if you register 1,000 sockets onto one `epoll` instance, you get: **1** `eventpoll`, **1,000** `epitem`s, and **1,000** callback entries (one in each socket's wait queue, each pointing back to its `epitem`, and through that `epitem`, to the shared `eventpoll`). The relationship is strictly `eventpoll : epitem = 1 : N`.

**It lives entirely in kernel space.** User space never sees it. The only handle you have is the integer FD returned by `epoll_create1()`. The kernel maps `your int epfd → struct file → struct eventpoll` (in kernel heap). You cannot read its fields, take its address, or touch it directly — you only influence it through syscalls (`epoll_ctl`, `epoll_wait`). Same goes for `epitem`.

Here is the simplified structure of `eventpoll` from the kernel source:

```c
struct eventpoll {
    spinlock_t lock;                    /* protects the struct */
    struct mutex mtx;                   /* for deep sleep */

    wait_queue_head_t wq;               /* ← where epoll_wait() sleepers wait */
    wait_queue_head_t poll_wait;

    struct list_head rdllist;           /* ← THE READY LIST (ready epitems) */

    struct rb_root_cached rbr;          /* ← RB-tree of ALL registered epitems */

    struct epitem *ovflist;             /* overflow list for re-entrancy */
    struct file *file;                  /* the struct file for this epoll fd */
    struct wakeup_source *ws;           /* prevents system auto-suspend */
    struct eventpoll *parent;           /* for epoll-on-epoll (nested) */
    /* ... a few more fields ... */
};
```

The three fields that matter most for understanding the design:

| Field | Type | Purpose |
|---|---|---|
| `rbr` | red-black tree | Stores **every `epitem`** you registered, indexed by FD. O(log n) lookup so `epoll_ctl MOD/DEL` is fast. |
| `rdllist` | doubly-linked list | Stores only the **ready** `epitem`s — i.e., FDs whose callbacks fired. O(1) insertion when data arrives; this is what `epoll_wait` drains and hands back to you. |
| `wq` | wait queue | Where **your process** sleeps when it calls `epoll_wait()`. |

So the picture inside one `eventpoll` is:

```
eventpoll
   ├── rbr        : ALL registered epitems (RB-tree — for fast ctl lookup)
   ├── rdllist    : READY epitems only    (linked list — drained by epoll_wait)
   └── wq         : sleeping processes   (wait queue — woken by ep_poll_callback)
```

Notice the clean division of labour: the RB-tree is the *registry* (everything you ever added), the `rdllist` is the *inbox* (just the things that have data right now), and `wq` is the *sleeping bag* (processes waiting for the inbox to fill). When a packet arrives, `ep_poll_callback` does two things in one shot: it moves the relevant `epitem` from `rbr` membership into `rdllist`, and it wakes whoever is on `wq`.

This also explains why your notes keep saying the socket only carries a *callback*, not your process and not the `epoll_fd`: the socket has no idea who cares about it — it just fires `ep_poll_callback`, and that callback knows how to find its `epitem`, which knows how to find its `eventpoll`, which knows who to wake up.

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

#### What Actually Sits in `wq`? (Threads, Not Epitems)
It is very easy to walk away from the above thinking that `rdllist` and `wq` are two flavours of the same thing — both lists of stuff "waiting." They are not. They hold **completely different kinds of objects**:

| Field | What it holds | Why |
|---|---|---|
| `rdllist` | **`epitem`s** (data descriptors — "these sockets are ready") | The kernel needs a list of *what* to report to you. |
| `wq` | **`task_struct`s** (live threads — "these threads want to be told") | The kernel needs a list of *who* to wake up. |

An `epitem` is just a struct — it does not execute code, it does not "wait." Only a *thread* can sleep, and only a thread can wake up. So `wq` holds threads, never epitems. Concretely, a Linux wait queue is a linked list of `wait_queue_entry` structs, each pointing at a `task_struct`. When your thread calls `epoll_wait()` and nothing is ready, the kernel:

1. Builds a `wait_queue_entry` whose `.private = current` (the current thread).
2. Sets the thread's state to `TASK_INTERRUPTIBLE` (sleeping).
3. Appends that entry onto `eventpoll->wq`.
4. Calls `schedule()` — voluntarily yields the CPU.

So at that moment, **your thread literally sits inside `eventpoll->wq`.** That is its home while asleep.

#### What "Wake Up" Mechanically Means
"Wake up" is a stricter term than people assume. It does **not** mean "run the process." It is a single state transition:

```
sleeping  (TASK_INTERRUPTIBLE)
      │
      │  ep_poll_callback fires
      │  → calls wake_up(eventpoll->wq)
      ▼
runnable  (TASK_RUNNABLE)            ← this is all "wake up" means
      │
      │  CPU scheduler eventually picks it
      ▼
actually executing on a CPU core
      │
      ▼
epoll_wait() returns with the ready FDs
```

"Wake up" = flip the thread's state from sleeping to runnable **and** add it to the scheduler's runqueue. The scheduler — a separate part of the kernel — then decides when the thread actually gets a core. There can be a delay (microseconds, usually) between "woken" and "running" depending on system load. When the thread finally runs again, it returns from `schedule()`, removes its entry from `wq`, drains `rdllist` into your user-space `events` array, and `epoll_wait()` returns.

#### "One Epoll → One Process" — True in the Simple Case, Not Enforced by the Kernel
The classic single-threaded server (Redis, Node.js) does map cleanly to "one epoll ↔ one sleeping thread." That is a fine starting mental model. But the kernel does **not** enforce it. The real rules:

- **A process can own zero, one, or many epoll instances.** Each `epoll_create1()` allocates a fresh `eventpoll`. Call it 5 times, you have 5 epolls, each with its own `wq` and `rdllist`.
- **Multiple threads in the same process can share one epoll.** Threads share the FD table, so thread B can use thread A's epoll fd. If 3 threads all call `epoll_wait()` on the same epoll simultaneously, then 3 entries sit in that epoll's `wq` at once. When a packet arrives, the kernel's wake-up logic (using `WQ_FLAG_EXCLUSIVE`) typically wakes only *one* of them — this is the kernel's built-in defence against the thundering herd problem (see §9).
- **Multiple processes can share one epoll** — after `fork()`, or by passing the fd over a Unix domain socket. Both children end up sleeping in the *same* `wq`.

So the accurate statement is: **`wq` is "whoever is currently blocked in `epoll_wait` on this epoll instance, right now."** In the single-threaded case that's one thread; in the general case it's whatever set of threads happen to be sleeping there.

#### Why `wq` Is a *List* — The Multi-Threaded Consideration
Notice something structural: `wq` is a **list** of waiters, not a single `task_struct *owner` pointer. That choice is significant. If `epoll` had been designed purely for single-threaded servers, a single owner field would have been enough — you'd just store "the one thread that cares" and call `wake_up_process(owner)` when data arrives. No list needed.

The reason `wq` is a *collection* is precisely the multi-threaded case: when several threads can call `epoll_wait()` on the same `eventpoll` concurrently, they all need a place to park themselves, and the kernel needs a place to find them when a callback fires. The list is the structural feature that makes multi-thread/process sharing possible at all. Strip away multi-threading from the design requirements and `wq` collapses back into a single pointer.

In other words: `rdllist` exists to answer *"what is ready?"*, and `wq` exists as a *list* (rather than a single field) specifically to answer *"who are the waiters?"* in the case where there can be more than one.

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

### What's Actually Inside the `sk_wq` Entry?
It is tempting to think the socket wait queue entry carries everything needed to wake a thread — both *which epitem to add to the ready list* and *which thread to wake up*. **It does not.** The `sk_wq` entry only knows the epitem side of the story. The thread side lives in a separate queue (`eventpoll->wq`).

When you register an FD with `epoll_ctl(EPOLL_CTL_ADD)`, the kernel creates a `struct eppoll_entry` — the bridge between a socket and an `epitem` — and inserts it into the socket's `sk_wq`:

```c
struct eppoll_entry {
    struct list_head    llink;     // links into eventpoll->poll_wait
    struct epitem      *base;      // ← pointer back to the epitem
    wait_queue_entry_t  wait;      // the entry that actually sits in sk_wq
    wait_queue_head_t  *whead;     // back-pointer to the socket's wait queue head
};
```

Inside the embedded `wait_queue_entry_t`:
- `.func  = ep_poll_callback` — the function the kernel will invoke when data arrives.
- `.private` — points back at the `eppoll_entry` (so the callback can recover the `epitem` via `container_of`).

That is the **full extent** of what the entry carries: a callback function + a pointer to the `epitem`. No `task_struct`, no thread reference, no PID.

#### Why the Split?
The socket is deliberately decoupled from processes. A socket doesn't care whether 0, 1, or 50 threads are interested in it — it only records "*someone* cares, here's their callback." The mapping from "epoll instance → threads currently waiting" is maintained separately, *inside* the `eventpoll`'s `wq`, and it changes every time a thread enters or leaves `epoll_wait()`.

If the thread pointer were baked into the `sk_wq` entry, the kernel would have to rewrite socket state on every sleep/wake — and the multi-threaded case (many threads sharing one epoll, but only one `sk_wq` entry per epoll instance) would be impossible to represent.

#### The Indirection Chain When Data Arrives
```
sk_wq entry  ──knows──>  epitem  ──knows──>  eventpoll
   (callback)                                          │
                                                       ├──> rdllist  (epitem added here)
                                                       └──> wq       (threads woken from here)
```

So the callback discovers *who to wake* indirectly: it follows `epitem → eventpoll`, and then `wake_up(eventpoll->wq)` walks **that** list — the list of `task_struct`s sitting in `epoll_wait()` — to flip their state from sleeping to runnable.

| Question | Answered by | Holds |
|---|---|---|
| *Which epitem is ready?* | `sk_wq` entry | callback + `epitem` pointer |
| *Which thread(s) to wake?* | `eventpoll->wq` | `task_struct`s |

### The True Flow of an Interrupt
What happens if your application is awake and processing 5 File Descriptors, and suddenly data arrives for a 6th File Descriptor?

1. **Hardware Preemption:** The Network Interface Card (NIC) sends an electrical signal to the CPU (a hardware interrupt). The CPU physically stops executing your User Space application and jumps into the Kernel's Interrupt Service Routine (ISR). *(If you have multiple cores, this interrupt might be handled on a different core while your app continues running uninterrupted).*
2. **Updating the Ready List:** The Kernel reads the data from the NIC. It checks the socket's **Wait Queue** and sees your `epoll` instance is registered there. The Kernel then silently adds this 6th FD to your `epoll` instance's internal **Ready List** in Kernel memory.
3. **Resuming the Application:** The CPU finishes the interrupt handling and resumes your Application. Your application is completely unaware it was paused.
4. **The Snapshot:** Your application does *not* instantly see the 6th FD. When `epoll_wait()` returned the original 5 FDs, it returned a **static snapshot** (an array). Your application will only see the 6th FD the *next time* the infinite loop spins around and calls `epoll_wait()` again.

**The Golden Rule of Event Loops:** Because of this static snapshot behavior, you must **never block the event loop**. If processing a single FD takes 5 seconds, the Kernel will quietly stack up hundreds of new FDs in the Ready List, but your application will be completely blind to them until those 5 seconds are up and `epoll_wait()` is called again.
