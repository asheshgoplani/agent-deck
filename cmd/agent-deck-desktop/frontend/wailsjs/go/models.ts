export namespace main {
	
	export class GroupInfo {
	    name: string;
	    path: string;
	    sessionCount: number;
	    totalCount: number;
	    level: number;
	    hasChildren: boolean;
	    expanded: boolean;
	
	    static createFrom(source: any = {}) {
	        return new GroupInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.sessionCount = source["sessionCount"];
	        this.totalCount = source["totalCount"];
	        this.level = source["level"];
	        this.hasChildren = source["hasChildren"];
	        this.expanded = source["expanded"];
	    }
	}
	export class LaunchConfigInfo {
	    key: string;
	    name: string;
	    tool: string;
	    description: string;
	    dangerousMode: boolean;
	    mcpConfigPath: string;
	    mcpNames?: string[];
	    extraArgs: string[];
	    isDefault: boolean;
	
	    static createFrom(source: any = {}) {
	        return new LaunchConfigInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.name = source["name"];
	        this.tool = source["tool"];
	        this.description = source["description"];
	        this.dangerousMode = source["dangerousMode"];
	        this.mcpConfigPath = source["mcpConfigPath"];
	        this.mcpNames = source["mcpNames"];
	        this.extraArgs = source["extraArgs"];
	        this.isDefault = source["isDefault"];
	    }
	}
	export class ProjectInfo {
	    path: string;
	    name: string;
	    score: number;
	    hasSession: boolean;
	    tool: string;
	    sessionId: string;
	
	    static createFrom(source: any = {}) {
	        return new ProjectInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.name = source["name"];
	        this.score = source["score"];
	        this.hasSession = source["hasSession"];
	        this.tool = source["tool"];
	        this.sessionId = source["sessionId"];
	    }
	}
	export class QuickLaunchFavorite {
	    name: string;
	    path: string;
	    tool: string;
	    shortcut?: string;
	
	    static createFrom(source: any = {}) {
	        return new QuickLaunchFavorite(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.tool = source["tool"];
	        this.shortcut = source["shortcut"];
	    }
	}
	export class SSHHostStatus {
	    hostId: string;
	    connected: boolean;
	    lastError?: string;

	    static createFrom(source: any = {}) {
	        return new SSHHostStatus(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hostId = source["hostId"];
	        this.connected = source["connected"];
	        this.lastError = source["lastError"];
	    }
	}
	export class SavedLayoutNode {
	    type: string;
	    id?: string;
	    direction?: string;
	    ratio?: number;
	    children?: SavedLayoutNode[];

	    static createFrom(source: any = {}) {
	        return new SavedLayoutNode(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.type = source["type"];
	        this.id = source["id"];
	        this.direction = source["direction"];
	        this.ratio = source["ratio"];
	        this.children = this.convertValues(source["children"], SavedLayoutNode);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SavedLayout {
	    id: string;
	    name: string;
	    layout?: SavedLayoutNode;
	    shortcut?: string;
	    createdAt: number;
	    updatedAt?: number;

	    static createFrom(source: any = {}) {
	        return new SavedLayout(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.layout = this.convertValues(source["layout"], SavedLayoutNode);
	        this.shortcut = source["shortcut"];
	        this.createdAt = source["createdAt"];
	        this.updatedAt = source["updatedAt"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SessionMetadata {
	    hostname: string;
	    cwd: string;
	    gitBranch: string;

	    static createFrom(source: any = {}) {
	        return new SessionMetadata(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hostname = source["hostname"];
	        this.cwd = source["cwd"];
	        this.gitBranch = source["gitBranch"];
	    }
	}

	export class SessionInfo {
	    id: string;
	    title: string;
	    customLabel?: string;
	    projectPath: string;
	    groupPath: string;
	    tool: string;
	    status: string;
	    tmuxSession: string;
	    isRemote: boolean;
	    remoteHost?: string;
	    gitBranch?: string;
	    isWorktree?: boolean;
	    gitDirty?: boolean;
	    gitAhead?: number;
	    gitBehind?: number;
	    // Go type: time
	    lastAccessedAt?: any;
	    launchConfigName?: string;
	    loadedMcps?: string[];
	    dangerousMode?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SessionInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.title = source["title"];
	        this.customLabel = source["customLabel"];
	        this.projectPath = source["projectPath"];
	        this.groupPath = source["groupPath"];
	        this.tool = source["tool"];
	        this.status = source["status"];
	        this.tmuxSession = source["tmuxSession"];
	        this.isRemote = source["isRemote"];
	        this.remoteHost = source["remoteHost"];
	        this.gitBranch = source["gitBranch"];
	        this.isWorktree = source["isWorktree"];
	        this.gitDirty = source["gitDirty"];
	        this.gitAhead = source["gitAhead"];
	        this.gitBehind = source["gitBehind"];
	        this.lastAccessedAt = this.convertValues(source["lastAccessedAt"], null);
	        this.launchConfigName = source["launchConfigName"];
	        this.loadedMcps = source["loadedMcps"];
	        this.dangerousMode = source["dangerousMode"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SessionsWithGroups {
	    sessions: SessionInfo[];
	    groups: GroupInfo[];

	    static createFrom(source: any = {}) {
	        return new SessionsWithGroups(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sessions = this.convertValues(source["sessions"], SessionInfo);
	        this.groups = this.convertValues(source["groups"], GroupInfo);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

